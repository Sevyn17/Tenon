// Package bind 绑定式传输：宿主把一个方法 Call(cmd, args) 绑给前端、把事件推给前端（不开网络端口），
// 如 Wails 桌面端。走 protocol 2 的连接（appapi.Conn）；切工作区时用 SwapAPI 换上新的 App。
package bind

// 绑定给前端的唯一服务：Call(cmd, args) 走 protocol 2 的连接（appapi.Conn）；事件经 Wails 事件转发（不开网络端口）。
// 桌面端只有一个前端窗口，整个进程一个连接；身份固定为本机操作者（06 10.8）。

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/guard"
	"github.com/Sevyn17/tenon/appapi/wire"
)

// EventName 前端订阅的 Wails 事件名；数据为 wire.Envelope。
const EventName = "tenon:event"

// ReconnectedEvent SwapAPI 换上新 App 之后发的 Wails 事件（客户端改进 M5）；数据为空对象。
// 旧连接上的订阅已全部作废，前端 transport 收到后按 WebSocket 重连的同一条路径通知上层
// （状态 reconnecting → open，再发本地事件 local.reconnected），上层重新 hello、重新订阅。
// 能力 reconnect_event（appapi.CapReconnectEvent）表示有这个通知。
//
// 用单独的事件名而不是在 EventName 里放一种 kind：EventName 只装协议事件（wire.Envelope，kind 是 06 10.5
// 的公开事件表），这条是传输层的连接通知；混进去的话旧前端会把它当成未知事件交给订阅方。
const ReconnectedEvent = "tenon:reconnected"

// Service 桌面端绑定服务。方法签名保持简单（字符串进出），前端统一按 JSON 解析。
type Service struct {
	api  *appapi.App
	emit func(name string, data any) // 发给前端；测试里替换

	mu      sync.Mutex
	conn    *appapi.Conn
	connFor *appapi.App // conn 是在哪个 App 上建的
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewService 用应用接口层构造服务。
func NewService(api *appapi.App, emit func(name string, data any)) *Service {
	return &Service{api: api, emit: emit}
}

// SwapAPI 换上新的应用接口层（切工作区、workspace.reload 后调用）：关掉旧连接，事件转发在新连接上重启；前端随后重新 hello。
//
// 换 api 与丢弃旧连接在同一把锁里做（D-11）：原先先 StopEvents、解锁、再设 s.api，中间进来的 Call 会用旧的、
// 已关闭的 App 新建连接并存下，之后一直连在旧运行时上，只能重启进程。
func (s *Service) SwapAPI(api *appapi.App) {
	s.mu.Lock()
	running := s.cancel != nil
	cancel, done, c := s.cancel, s.done, s.conn
	s.api = api
	s.cancel, s.done, s.conn, s.connFor = nil, nil, nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if c != nil {
		c.Close()
	}
	if running {
		s.StartEvents()
	}
	// 新连接、事件转发都就位之后再通知前端：前端随后的 hello、session.subscribe 落在新 App 上，
	// 重新订阅的回放也经新连接的转发送达。没在转发事件时也发（前端没监听就没有效果）。
	s.emit(ReconnectedEvent, struct{}{})
}

// API 当前的应用接口层。
func (s *Service) API() *appapi.App {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.api
}

// connection 当前连接；StartEvents 之前调用时也建一个（命令可以先于事件转发）。
func (s *Service) connection() *appapi.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connLocked()
}

// connLocked 见 connection；调用方持有 s.mu。连接不是在当前 App 上建的（刚换过）就丢掉重建。
func (s *Service) connLocked() *appapi.Conn {
	if s.conn == nil || s.connFor != s.api {
		if old := s.conn; old != nil {
			go old.Close()
		}
		s.conn, s.connFor = s.api.NewConn(context.Background(), appapi.LocalPrincipal), s.api
		s.conn.AddCapabilities(appapi.CapReconnectEvent) // SwapAPI 之后发 ReconnectedEvent
	}
	return s.conn
}

// Call 执行一条 protocol 2 命令。args 为 JSON 文本（空 = 无参数）；成功返回结果 JSON。
// 失败时错误文本是 wire.Error 的 JSON（{code, message, retry, details}），前端解析出错误码。
func (s *Service) Call(cmd string, args string) (string, error) {
	var raw json.RawMessage
	if args != "" {
		if !json.Valid([]byte(args)) {
			return "", encodeErr(appapi.Errorf(wire.CodeBadRequest, "参数不是合法 JSON"))
		}
		raw = json.RawMessage(args)
	}
	res, err := s.connection().Call(context.Background(), cmd, raw)
	if err != nil {
		return "", encodeErr(appapi.AsError(err))
	}
	out, err := json.Marshal(res)
	if err != nil {
		return "", encodeErr(appapi.Errorf(wire.CodeInternal, "结果编码失败：%v", err))
	}
	return string(out), nil
}

func encodeErr(e *appapi.Error) error {
	b, _ := json.Marshal(e)
	return errors.New(string(b))
}

// StartEvents 把连接的出站事件转发给前端，直到 StopEvents。背压在 Conn 里处理（合并、丢弃、断开订阅并发
// session.lagged，前端据此重新订阅）。
func (s *Service) StartEvents() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	c := s.connLocked() // 同一把锁里取连接：中间换了 App 的话转发不会挂在旧连接上
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		defer guard.Recover("bind事件泵", func() { c.Close() })
		for {
			evs, err := c.Next(ctx)
			if err != nil {
				return
			}
			for _, e := range evs {
				s.emit(EventName, e)
			}
		}
	}(s.done)
}

// StopEvents 停止转发、关闭连接并等待协程退出。
func (s *Service) StopEvents() {
	s.mu.Lock()
	cancel, done, c := s.cancel, s.done, s.conn
	s.cancel, s.done, s.conn, s.connFor = nil, nil, nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if c != nil {
		c.Close()
	}
}
