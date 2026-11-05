package main

// 绑定给前端的唯一服务：Call(cmd, args) 走 protocol 2 的连接（appapi.Conn）；事件经 Wails 事件转发（不开网络端口）。
// 桌面端只有一个前端窗口，整个进程一个连接；身份固定为本机操作者（06 10.8）。

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"ybase/appapi"
	"ybase/appapi/wire"
)

// EventName 前端订阅的 Wails 事件名；数据为 wire.Envelope。
const EventName = "ybase:event"

// Service 桌面端绑定服务。方法签名保持简单（字符串进出），前端统一按 JSON 解析。
type Service struct {
	api  *appapi.App
	emit func(name string, data any) // 发给前端；测试里替换

	mu     sync.Mutex
	conn   *appapi.Conn
	cancel context.CancelFunc
	done   chan struct{}
}

// NewService 用应用接口层构造服务。
func NewService(api *appapi.App, emit func(name string, data any)) *Service {
	return &Service{api: api, emit: emit}
}

// connection 当前连接；StartEvents 之前调用时也建一个（命令可以先于事件转发）。
func (s *Service) connection() *appapi.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		s.conn = s.api.NewConn(context.Background(), appapi.LocalPrincipal)
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
	c := s.connection()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
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
	s.cancel, s.done, s.conn = nil, nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if c != nil {
		c.Close()
	}
}
