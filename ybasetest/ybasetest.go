// Package ybasetest 底座测试套件（框架设计 10 10.3，T1b-2d 最小版）：
// 内存存储 + 脚本模型 + 断言。用法见 10.3：
//
//	app := ybasetest.New(t, ybasetest.Model(
//	    ybasetest.Reply("先查订单").CallTool("order_query", `{"id":"A1"}`),
//	    ybasetest.Reply("已发货，明天到"),
//	))
//	reply, err := app.Ask(ctx, "support", "A1 到哪了", nil)
//	app.AssertToolCalled("order_query")
package ybasetest

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"ybase"
	"ybase/runtime/modelhub"
	"ybase/runtime/session"
	"ybase/runtime/store"
	"ybase/runtime/store/memstore"
	"ybase/runtime/store/sqlite"
)

const (
	modelName = "test"
	provider  = "scripted"
	protocol  = "scripted"
)

// Step 脚本模型的一步：返回 text，可选地发起工具调用。
type Step struct {
	text  string
	calls []toolCall
}

type toolCall struct{ name, args string }

// Reply 一条只说话的预设回复。
func Reply(text string) Step { return Step{text: text} }

// CallTool 让这一步先调用一个工具（下一条模型回复接着来）。
func (s Step) CallTool(name, args string) Step {
	s.calls = append(s.calls, toolCall{name: name, args: args})
	return s
}

// Model 脚本模型选项：按顺序返回预设；被调用次数超过脚本步数即失败。
func Model(steps ...Step) ybase.Option {
	ad := &scriptAdapter{steps: steps}
	cat := modelhub.NewCatalog()
	if err := cat.Add(modelhub.LayerUser, "ybasetest", modelhub.CatalogFile{
		Provider: map[string]modelhub.Provider{provider: {Protocol: protocol}},
		Model: map[string]modelhub.Model{
			modelName: {Provider: provider, ContextWindow: 100000, MaxOutput: 8192},
		},
	}); err != nil {
		panic(err)
	}
	hub := modelhub.New(cat, func(string) (string, error) { return "", nil }, nil)
	if err := hub.RegisterAdapter(ad); err != nil {
		panic(err)
	}
	return ybase.WithModels(hub, modelName)
}

// App 测试用的应用：开始运行后由 Cleanup 关闭。
type App struct {
	*ybase.App
	t  *testing.T
	mu sync.Mutex
	// ids 经本包装 Ask 的会话，断言用。
	ids []string
}

// New 用内存存储 + 脚本模型装配并启动应用；opts 覆盖缺省项。
func New(t *testing.T, opts ...ybase.Option) *App {
	t.Helper()
	return NewOn(t, memstore.New(), opts...)
}

// Backend 一种存储实现（Stores 给出 SQLite 与内存两种）。
type Backend struct {
	Name string
	Open func(t *testing.T) store.Store
}

// Stores 验收用例要在两种存储上都跑（计划/01 第三节 v1 验收第 1 条）：
//
//	for _, b := range ybasetest.Stores() {
//		t.Run(b.Name, func(t *testing.T) { app := ybasetest.NewOn(t, b.Open(t), ...) })
//	}
func Stores() []Backend {
	return []Backend{
		{Name: "memstore", Open: func(*testing.T) store.Store { return memstore.New() }},
		{Name: "sqlite", Open: func(t *testing.T) store.Store {
			t.Helper()
			db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), sqlite.Options{})
			if err != nil {
				t.Fatalf("sqlite.Open: %v", err)
			}
			return db // 由 App.Close 关闭（WithStore 的存储归应用所有）
		}},
	}
}

// NewOn 同 New，存储由调用方给（Stores 里的一种）。
func NewOn(t *testing.T, st store.Store, opts ...ybase.Option) *App {
	t.Helper()
	all := []ybase.Option{
		ybase.Default(),
		ybase.WithStore(st),
		ybase.WithWorkdir(t.TempDir()),
		Model(), // 缺省空脚本：没写 Model(...) 时调用即失败
	}
	all = append(all, opts...)
	app, err := ybase.New(all...)
	if err != nil {
		t.Fatalf("ybase.New: %v", err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	return &App{App: app, t: t}
}

// Ask 跑一轮并记录会话 id。
func (a *App) Ask(ctx context.Context, agentName, text string, meta ybase.Meta) (*ybase.Reply, error) {
	a.t.Helper()
	r, err := a.App.Ask(ctx, agentName, text, meta)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.ids = append(a.ids, r.SessionID)
	a.mu.Unlock()
	return r, nil
}

// AssertToolCalled 断言本轮里模型调用过名为 name 的工具。
func (a *App) AssertToolCalled(name string) {
	a.t.Helper()
	a.mu.Lock()
	ids := append([]string(nil), a.ids...)
	a.mu.Unlock()
	for _, id := range ids {
		entries, err := a.History(context.Background(), id)
		if err != nil {
			a.t.Fatalf("读会话 %s: %v", id, err)
		}
		for _, e := range entries {
			if e == nil || e.Item.Assistant == nil {
				continue
			}
			for _, c := range e.Item.Assistant.ToolCalls {
				if c.Name == name {
					return
				}
			}
		}
	}
	a.t.Fatalf("断言失败：工具 %s 没有被调用（会话 %v）", name, ids)
}

// scriptAdapter 按脚本逐次返回；记调用次数。
type scriptAdapter struct {
	steps []Step
	mu    sync.Mutex
	calls int
}

func (a *scriptAdapter) Protocol() string { return protocol }

func (a *scriptAdapter) Stream(ctx context.Context, r *modelhub.Request, ep modelhub.Endpoint) <-chan modelhub.Event {
	a.mu.Lock()
	n := a.calls
	a.calls++
	a.mu.Unlock()
	ch := make(chan modelhub.Event, 8)
	go func() {
		defer close(ch)
		if n >= len(a.steps) {
			ch <- modelhub.Event{Failed: &modelhub.Failure{
				Code:    modelhub.Code("script_exhausted"),
				Message: fmt.Sprintf("脚本模型第 %d 次被调用，但脚本只有 %d 步", n+1, len(a.steps)),
			}}
			return
		}
		s := a.steps[n]
		reply := session.AssistantItem{
			Model:   ep.Model,
			Content: session.Content{session.NewTextPart(s.text)},
			Usage:   session.Usage{Input: 10, Output: 4, CacheRead: 6, Source: "Reported"},
		}
		for _, c := range s.calls {
			args := c.args
			if args == "" {
				args = "{}"
			}
			reply.ToolCalls = append(reply.ToolCalls, session.ToolCall{Name: c.name, Args: json.RawMessage(args)})
		}
		stop := session.StopReason{V: "EndTurn"}
		if len(s.calls) > 0 {
			stop = session.StopReason{V: "ToolUse"}
		}
		ch <- modelhub.Event{Done: &modelhub.Done{Reply: reply, Stop: stop}}
	}()
	return ch
}
