package workgroup

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// blockModel 成员回合（用户消息含 "慢活"）阻塞到 ctx 取消；其余回显。
type blockModel struct {
	mu      sync.Mutex
	started chan struct{}
	seen    []string
}

func (m *blockModel) Stream(ctx context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	last := ""
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			last = ""
			for _, p := range msg.Content {
				last += p.Text
			}
		}
	}
	m.mu.Lock()
	m.seen = append(m.seen, last)
	m.mu.Unlock()
	if strings.Contains(last, "慢活") {
		m.started <- struct{}{}
		<-ctx.Done()
		return session.AssistantItem{}, session.StopReason{}, ctx.Err()
	}
	return session.AssistantItem{Content: session.Content{session.NewTextPart("re:" + last)}}, session.StopReason{V: "stop"}, nil
}

func (m *blockModel) has(sub string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.seen {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// 领导能用 team_stop 中止正在工作的成员，结果照常送回（审查 P1-27：原先只能等 30 分钟超时）。
func TestTeamStopCancelsMember(t *testing.T) {
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	m := &blockModel{started: make(chan struct{}, 4)}
	h := &host.Host{DB: db}
	var g *Group
	mk := func(leader bool) host.Factory {
		return func(id, def string) (*agent.Agent, *session.Config, error) {
			hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
			ts := g.MemberTools()
			if leader {
				ts = g.LeaderTools()
			}
			for _, tl := range ts {
				if err := hub.Register(tl); err != nil {
					return nil, nil, err
				}
			}
			a := &agent.Agent{Store: db, SessionID: id, Model: m, Hub: hub}
			return a, &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil)), Policy: session.DefaultPolicy()}, nil
		}
	}
	h.New = mk(true)
	tasks, runs := openBoards(t, db)
	g, err := New(context.Background(), h, nil, tasks, runs, db, mk(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	ctx := context.Background()
	if _, err := h.Open(ctx, "L"); err != nil {
		t.Fatal(err)
	}
	if o := call(t, g, g.LeaderTools(), "team_spawn", "L", `{"name":"alice","task":"慢活"}`); o.Status != session.ToolOK {
		t.Fatal(txt(o))
	}
	select {
	case <-m.started:
	case <-time.After(5 * time.Second):
		t.Fatal("成员回合没开始")
	}
	if o := call(t, g, g.LeaderTools(), "team_stop", "L/m-alice", `{"name":"alice"}`); o.Status != session.ToolError {
		t.Fatal("成员不应能中止成员")
	}
	if o := call(t, g, g.LeaderTools(), "team_stop", "L", `{"name":"alice"}`); o.Status != session.ToolOK || !strings.Contains(txt(o), "已中止") {
		t.Fatalf("team_stop: %q", txt(o))
	}
	done := make(chan struct{})
	go func() { g.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("中止后成员回合应很快结束")
	}
	hd, _ := h.Get("L")
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if !m.has("[成员 alice") {
		t.Fatalf("领导应收到成员结果：%v", m.seen)
	}
	if o := call(t, g, g.LeaderTools(), "team_stop", "L", `{"name":"alice"}`); !strings.Contains(txt(o), "没有在工作") {
		t.Fatalf("已结束的成员：%q", txt(o))
	}
}
