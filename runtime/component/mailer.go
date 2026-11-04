package component

import (
	"context"
	"errors"
	"fmt"

	"ybase/runtime/mail"
)

// Sender 内核投递出口（由宿主实现）。装配期通过 service:host 注入。
type Sender interface {
	Deliver(ctx context.Context, env mail.Envelope) error
}

// serviceHost 与 runtime.ServiceHost 同值；plugin 不能 import runtime（runtime 依赖 plugin）。
const serviceHost = "service:host"

// Mailer 组件发信句柄：from 固定为本组件，调用方不能伪造发送者。
type Mailer struct {
	from mail.Address
	host Sender
}

// Mailer 取当前组件的发信句柄（P4）。没有注入投递服务时 Send 返回错误。
func (cx *Registrar) Mailer() *Mailer {
	m := &Mailer{from: mail.Address{Kind: mail.AddrComponent, ID: cx.plugin}}
	if v, ok := cx.asm.values[serviceHost]; ok {
		if s, ok := v.(Sender); ok {
			m.host = s
		}
	}
	return m
}

// From 本组件地址（component:<组件名>）。
func (m *Mailer) From() mail.Address { return m.from }

// Send 以组件身份发一封信封（内核写入 origin=agent）。
func (m *Mailer) Send(ctx context.Context, to mail.Address, body string) error {
	if m.host == nil {
		return errors.New("plugin: 没有内核投递服务，无法发信")
	}
	env := mail.Envelope{
		From:   m.from,
		Via:    m.from.ID,
		To:     to,
		Kind:   mail.KindRequest,
		Mode:   mail.ModeWake,
		Origin: mail.OriginAgent,
		Body:   body,
	}
	if err := env.Normalize(); err != nil {
		return err
	}
	if err := m.host.Deliver(ctx, env); err != nil {
		return fmt.Errorf("plugin: 发信给 %s：%w", to, err)
	}
	return nil
}
