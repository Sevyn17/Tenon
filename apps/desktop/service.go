package main

// 绑定给前端的唯一服务：Call(cmd, args) 透传 appapi.Call；事件经 Wails 事件转发（不开网络端口）。

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"ybase-kernel/core/appapi"
	"ybase-kernel/core/bus"
)

// EventName 前端订阅的 Wails 事件名；数据为 bus.Envelope。
const EventName = "ybase:event"

// Service 桌面端绑定服务。方法签名保持简单（字符串进出），前端统一按 JSON 解析。
type Service struct {
	api  *appapi.App
	emit func(name string, data any) // 发给前端；测试里替换

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewService 用应用接口层构造服务。
func NewService(api *appapi.App, emit func(name string, data any)) *Service {
	return &Service{api: api, emit: emit}
}

// Call 执行一条应用接口层命令。args 为 JSON 文本（空 = 无参数）；成功返回结果 JSON。
// 失败时错误文本是 appapi.Error 的 JSON（{code, message, retry}），前端解析出错误码。
func (s *Service) Call(cmd string, args string) (string, error) {
	var raw json.RawMessage
	if args != "" {
		if !json.Valid([]byte(args)) {
			return "", encodeErr(appapi.Errorf(appapi.CodeBadRequest, "参数不是合法 JSON"))
		}
		raw = json.RawMessage(args)
	}
	res, err := s.api.Call(context.Background(), cmd, raw)
	if err != nil {
		return "", encodeErr(appapi.AsError(err))
	}
	out, err := json.Marshal(res)
	if err != nil {
		return "", encodeErr(appapi.Errorf(appapi.CodeInternal, "结果编码失败：%v", err))
	}
	return string(out), nil
}

func encodeErr(e *appapi.Error) error {
	b, _ := json.Marshal(e)
	return errors.New(string(b))
}

// StartEvents 订阅全部会话的事件并转发给前端，直到 StopEvents 或总线关闭。
// 慢订阅者会被总线断开：断开后自动重新订阅（前端靠持久 entry 事件 / session.entries 补齐）。
func (s *Service) StartEvents() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		for ctx.Err() == nil {
			sub := s.api.Events(ctx, bus.Filter{})
			for e := range sub.C() {
				s.emit(EventName, e)
			}
			if errors.Is(sub.Err(), bus.ErrClosed) || ctx.Err() != nil {
				return
			}
			s.emit(EventName, bus.Envelope{V: bus.Version, Kind: "resync"}) // 提示前端重新拉取
		}
	}(s.done)
}

// StopEvents 停止转发并等待协程退出。
func (s *Service) StopEvents() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
