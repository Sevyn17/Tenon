package ybase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/appapi"
	"ybase-kernel/core/host"
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
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

// Start 启动后台工作：打开存储、跑 ready 点位、接上应用接口层。
func (a *App) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrNotStarted
	}
	if a.started {
		return nil
	}
	scope := plugin.NewRoot("ybase")
	if err := a.asm.Start(scope); err != nil {
		return fmt.Errorf("ybase: 启动组件: %w", err)
	}
	a.scope = scope
	db := a.handle.DB()
	if db == nil {
		_ = scope.Close(ctx)
		return fmt.Errorf("%w: 存储没有打开", ErrInvalid)
	}
	a.host.DB = db
	a.host.SleepAfter = a.sleepAfter
	bgCtx, cancel := context.WithCancel(context.Background())
	a.sleepStop = cancel
	if a.sleepAfter > 0 { // 休眠会话的兜底扫描（02 2.2）
		go a.host.ScanLoop(bgCtx, a.sleepAfter)
	}
	go a.host.Maintain(bgCtx, 0, 0) // 存储清理：过期的待回复、已派发外发、trace 事件
	// 启动时派发一次发件箱：上次进程在提交之后、派发之前退出时留下的外发。
	go func() { _, _ = a.host.FlushOutbox(bgCtx) }()
	kern, err := kr.ParseKernel(nil)
	if err != nil {
		return fmt.Errorf("ybase: 内核配置: %w", err)
	}
	col, err := kr.Collect(a.asm)
	if err != nil {
		return fmt.Errorf("ybase: 收集点位: %w", err)
	}
	// 组件登记的守卫规则（04 4.2、08 D4）。
	a.host.Guards = a.asm.GuardRules()
	a.host.Hooks.AfterTurn = func(id string, end session.EndReason) {
		for _, f := range col.AfterTurn {
			f(id, end)
		}
	}
	rb := &kr.Builder{
		DB:          db,
		Artifacts:   a.handle.Blobs(),
		Models:      a.models.Hub,
		Model:       a.models.Model,
		Workdir:     a.workdir,
		Policy:      func() session.Policy { return kern.Policy },
		Tools:       col.Tools,
		Layers:      col.Layers,
		Blocks:      col.Blocks,
		Items:       col.Items,
		Kernel:      &kern,
		Definitions: a.defs,
		Statuses:    col.Statuses,
		Schemas:     a.asm.AgentSchemas(),
	}
	rb.Hooks = func(*kr.SessionCx) agent.Hooks { return agent.Hooks{} }
	if _, err := appapi.New(appapi.Options{
		Host: a.host, Bus: a.bus, DB: db, Builder: rb,
		Model: a.models.Model, Workdir: a.workdir, Trusted: true,
	}); err != nil {
		return fmt.Errorf("ybase: 应用接口: %w", err)
	}
	for _, f := range col.Ready {
		if err := f(ctx); err != nil {
			return fmt.Errorf("ybase: ready: %w", err)
		}
	}
	a.builder = rb
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
	stop := a.sleepStop
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

// Ask 新开一个会话跑一轮：建会话（绑定义、写 meta）→ 投递用户消息 → 等回合结束 → 取最后一段助手文本。
func (a *App) Ask(ctx context.Context, agentName, text string, meta Meta) (*Reply, error) {
	a.mu.Lock()
	started, closed, rb := a.started, a.closed, a.builder
	a.mu.Unlock()
	if !started || closed {
		return nil, ErrNotStarted
	}
	if strings.TrimSpace(agentName) == "" {
		return nil, fmt.Errorf("%w: 定义名为空", ErrInvalid)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: 消息为空", ErrInvalid)
	}
	u, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("ybase: 生成会话 id: %w", err)
	}
	id := u.String()
	ag, cfg, err := rb.Build(id, agentName, false)
	if err != nil {
		return nil, err
	}
	sm := store.SessionMeta{ID: id, Creator: userFrom(ctx)}
	if ag.Def != nil {
		sm.AgentID, sm.AgentHash = ag.Def.ID, ag.Def.Version
	}
	if len(meta) > 0 {
		raw, err := json.Marshal(meta)
		if err != nil {
			return nil, fmt.Errorf("%w: meta: %v", ErrInvalid, err)
		}
		sm.Meta = raw
	}
	if _, err := ag.OpenAs(ctx, cfg, sm); err != nil {
		return nil, err
	}
	hd, err := a.host.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := hd.Do(host.Command{
		Kind: host.Send, ItemID: host.NewItemID(), Text: text,
		Trust: "trusted", Source: "user",
	}); err != nil {
		return nil, err
	}
	if err := hd.Wait(); err != nil {
		return nil, err
	}
	out, err := lastAssistant(ctx, a.handle, id)
	if err != nil {
		return nil, err
	}
	return &Reply{Text: out, SessionID: id}, nil
}

// History 读一个会话的条目（断言与回放用）。
func (a *App) History(ctx context.Context, id string) ([]*session.Entry, error) {
	a.mu.Lock()
	handle, started := a.handle, a.started
	a.mu.Unlock()
	if !started || handle == nil {
		return nil, ErrNotStarted
	}
	l, err := handle.db.Load(ctx, id, nil)
	if err != nil {
		return nil, err
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
