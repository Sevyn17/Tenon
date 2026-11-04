package ybase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ybase/appapi"
	"ybase/runtime/agentdef"
	"ybase/runtime/assemble"
	"ybase/runtime/component"
	"ybase/runtime/mail"
)

// Meta 会话业务 meta（写进会话行，组件经 *Ctx 读）。
type Meta map[string]any

// Reply 一次 Ask 的结果。
type Reply struct {
	// Text 最后一条助手消息的文本（可能为空：只调了工具没说话）。
	Text string
	// SessionID 本次会话 id（后续可用 Session 继续）。
	SessionID string
}

// Start 启动组件、接上会话构造器与 [kernel] 宿主参数（assemble.Wire，与应用同一条路径）、
// 接上应用接口层、跑 ready 点位，最后启动后台循环（兜底扫描、存储清理、启动时派发发件箱）。
// 后台循环放在构造器接上之后：派发发件箱会打开会话，要用 Host.New（审查 P1-51）。
func (a *App) Start(ctx context.Context) (err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrNotStarted
	}
	if a.started {
		return nil
	}
	scope := component.NewRoot("ybase")
	if err := a.asm.Start(scope); err != nil {
		_ = scope.Close(ctx)
		return fmt.Errorf("ybase: 启动组件: %w", err)
	}
	// 中途失败整体回滚：关组件作用域，再 Start 从头来（审查 P1-23：原先留着已启动的
	// 作用域和后台协程，再次 Start 会让组件启动两次）。
	defer func() {
		if err != nil {
			_ = scope.Close(context.WithoutCancel(ctx))
			a.scope = nil
		}
	}()
	a.scope = scope
	db := a.handle.DB()
	if db == nil {
		return fmt.Errorf("%w: 存储没有打开", ErrInvalid)
	}
	w, err := assemble.Wire(assemble.WireOptions{Asm: a.asm, Host: a.host, DB: db, Blobs: a.handle.Blobs(),
		Models: a.models, Workdir: a.workdir, Kernel: a.kern, Definitions: a.defs, MaxSteps: -1})
	if err != nil {
		return fmt.Errorf("ybase: 接线: %w", err)
	}
	if _, err := appapi.New(appapi.Options{
		Host: a.host, Bus: a.bus, DB: db, Builder: w.Builder,
		Model: a.models.Model, Workdir: a.workdir, Trusted: true,
	}); err != nil {
		return fmt.Errorf("ybase: 应用接口: %w", err)
	}
	if errs := w.Ready(ctx); len(errs) > 0 {
		return fmt.Errorf("ybase: ready: %w", errors.Join(errs...))
	}
	a.builder = w.Builder
	a.bgStop = w.Background()
	a.started = true
	return nil
}

// Close 停后台工作、关存储。可重复调用。
func (a *App) Close(ctx context.Context) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	scope := a.scope
	stop := a.bgStop
	a.mu.Unlock()
	var errs []error
	if stop != nil {
		stop()
	}
	if scope != nil {
		if err := scope.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if a.handle != nil && a.handle.db != nil {
		if err := a.handle.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	a.bus.Close()
	if len(errs) > 0 {
		return fmt.Errorf("ybase: 关闭: %w", errs[0])
	}
	return nil
}

// LocalUser 没有 WithUser 时的发送者（单机应用的本机用户）：信封 from 必须有 id，会话创建者记它。
const LocalUser = "local"

// Ask 新开一个会话跑一轮：建会话（绑定义、写 meta）→ 投递用户消息 → 等回合结束 → 取最后一段助手文本。
func (a *App) Ask(ctx context.Context, agentName, text string, meta Meta) (*Reply, error) {
	a.mu.Lock()
	started, closed := a.started, a.closed
	a.mu.Unlock()
	if !started || closed {
		return nil, ErrNotStarted
	}
	if err := checkTenant(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(agentName) == "" {
		return nil, fmt.Errorf("%w: 定义名为空", ErrInvalid)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: 消息为空", ErrInvalid)
	}
	// 走信封投递（审查 P1-24）：from = human:<用户>，经会话打开守卫、投递守卫与联系名单，
	// 和 CLI / 服务端 / 其他会话发来的信同一条路；会话只构造一次。
	user := userFrom(ctx)
	if user == "" {
		user = LocalUser
	}
	var raw json.RawMessage
	if len(meta) > 0 {
		b, err := json.Marshal(meta)
		if err != nil {
			return nil, fmt.Errorf("%w: meta: %v", ErrInvalid, err)
		}
		raw = b
	}
	id, err := a.host.DeliverAgent(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: user},
		To:   mail.Address{Kind: mail.AddrAgent, ID: agentName},
		Kind: mail.KindRequest, Origin: mail.OriginHuman, Body: text,
	}, raw)
	if err != nil {
		if errors.Is(err, agentdef.ErrNotFound) {
			return nil, fmt.Errorf("%w: 定义 %s", ErrNotFound, agentName)
		}
		return nil, wrapErr(err)
	}
	hd, ok := a.host.Get(id)
	if !ok {
		var err error
		if hd, err = a.host.Open(ctx, id); err != nil {
			return nil, wrapErr(err)
		}
	}
	if err := hd.Wait(); err != nil {
		return nil, wrapErr(err)
	}
	out, err := lastAssistant(ctx, a.handle, id)
	if err != nil {
		return nil, wrapErr(err)
	}
	return &Reply{Text: out, SessionID: id}, nil
}

// History 读一个会话的条目（断言与回放用）。
func (a *App) History(ctx context.Context, id string) ([]*Entry, error) {
	a.mu.Lock()
	handle, started := a.handle, a.started
	a.mu.Unlock()
	if !started || handle == nil {
		return nil, ErrNotStarted
	}
	if err := checkTenant(ctx); err != nil {
		return nil, err
	}
	l, err := handle.db.Load(ctx, id, nil)
	if err != nil {
		return nil, wrapErr(err)
	}
	if l == nil {
		return nil, fmt.Errorf("%w: 会话 %s", ErrNotFound, id)
	}
	return l.Snapshot.Entries, nil
}

// lastAssistant 读会话条目，取最后一条有文本的助手消息。
func lastAssistant(ctx context.Context, db *storeHandle, id string) (string, error) {
	l, err := db.DB().Load(ctx, id, nil)
	if err != nil {
		return "", err
	}
	if l == nil {
		return "", fmt.Errorf("%w: 会话 %s", ErrNotFound, id)
	}
	for i := len(l.Snapshot.Entries) - 1; i >= 0; i-- {
		e := l.Snapshot.Entries[i]
		if e == nil || e.Item.Assistant == nil {
			continue
		}
		var b strings.Builder
		for _, p := range e.Item.Assistant.Content {
			if p.Kind == "text" {
				b.WriteString(p.Text)
			}
		}
		if b.Len() > 0 {
			return b.String(), nil
		}
	}
	return "", nil
}
