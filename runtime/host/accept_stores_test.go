package host

// v1 验收第 1 条：T3（09 9.6）的验收用例在 SQLite 与内存两种存储上都过。
// 这里把五条验收按存储参数化跑一遍（各条的细节用例仍在 t3_test.go / guard_b_test.go，用 SQLite）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"tenon/runtime/agent"
	"tenon/runtime/mail"
	"tenon/runtime/session"
	"tenon/runtime/store"
	"tenon/runtime/store/memstore"
	"tenon/runtime/store/sqlite"
	"tenon/runtime/toolhub"
)

type acceptStore interface {
	Store
	store.ToolResultStore
}

func acceptStores(t *testing.T) map[string]func() acceptStore {
	return map[string]func() acceptStore{
		"memstore": func() acceptStore { return memstore.New() },
		"sqlite": func() acceptStore {
			db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			return db
		},
	}
}

// acceptHost 每个会话一个工具：id 以 "low" 开头的会话只有 read_file，其余有 echo。
func acceptHost(db acceptStore, executed *int) *Host {
	return &Host{DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		name := "echo"
		if strings.HasPrefix(id, "low") {
			name = "read_file"
		}
		if err := hub.Register(&toolhub.Tool{Spec: session.ToolSpec{Name: name, Schema: json.RawMessage(`{"type":"object"}`)},
			Risk: toolhub.Read, Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
				*executed++
				return toolhub.Text("echoed"), nil
			}}); err != nil {
			return nil, nil, err
		}
		ts := hub.SpecsFor(nil, 0)
		a := &agent.Agent{Store: db, SessionID: id, Model: &fakeModel{}, Hub: hub, Toolset: ts}
		return a, &session.Config{Model: "m", Tools: hub.Exposed(ts), Policy: session.DefaultPolicy()}, nil
	}}
}

func TestT3AcceptanceOnBothStores(t *testing.T) {
	for name, open := range acceptStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := open()
			executed := 0
			h := acceptHost(db, &executed)
			open1 := func(id string, meta store.SessionMeta) Handle {
				t.Helper()
				hd, err := h.open(ctx, id, "", h.New, func(ag *agent.Agent, cfg *session.Config) error {
					meta.ID = id
					_, err := ag.OpenAs(ctx, cfg, meta)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				return hd
			}

			// ① 低权限调高权限：high 不能执行 low 没有的工具（caps 由内核按发送会话写）。
			low := open1("low", store.SessionMeta{})
			high := open1("high", store.SessionMeta{ParentID: "low"})
			if err := db.IssueCorr(ctx, store.Pending{Corr: "c-high", SessionID: "high", ReplyTo: "session:low"}); err != nil {
				t.Fatal(err)
			}
			if err := low.Agent().Outbound(mail.Envelope{To: mail.Address{Kind: mail.AddrSession, ID: "high"},
				Kind: mail.KindReply, Corr: "c-high", Caps: []string{"echo"}, Body: "tool"}); err != nil {
				t.Fatal(err)
			}
			if err := low.Agent().Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := h.FlushOutbox(ctx); err != nil {
				t.Fatal(err)
			}
			if err := high.Wait(); err != nil {
				t.Fatal(err)
			}
			if executed != 0 || !strings.Contains(toolResults(high.Agent()), "不在本轮授权范围") {
				t.Fatalf("① 高权限会话不应执行发送方没有的工具：executed=%d %s", executed, toolResults(high.Agent()))
			}

			// ② 伪造 / 重放的 corr 被拒。
			s1 := open1("s1", store.SessionMeta{Creator: "u1"})
			if err := db.IssueCorr(ctx, store.Pending{Corr: "c1", SessionID: "s1", ReplyTo: "agent:worker"}); err != nil {
				t.Fatal(err)
			}
			reply := func(from string) error {
				return h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrAgent, ID: from},
					To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindReply, Corr: "c1", Body: "结果"})
			}
			if err := reply("other"); !errors.Is(err, store.ErrCorrRejected) {
				t.Fatalf("② 伪造发送者应拒：%v", err)
			}
			if err := reply("worker"); err != nil {
				t.Fatalf("② 正确回信：%v", err)
			}
			if err := reply("worker"); !errors.Is(err, store.ErrCorrRejected) {
				t.Fatalf("② 重放应拒：%v", err)
			}
			_ = s1.Wait()

			// ③ 不在联系名单的互投被拒（非创建者、agent 无回信直投、假 corr）。
			to := mail.Address{Kind: mail.AddrSession, ID: "s1"}
			for _, env := range []mail.Envelope{
				{From: mail.Address{Kind: mail.AddrHuman, ID: "u2"}, To: to, Body: "x"},
				{From: mail.Address{Kind: mail.AddrAgent, ID: "x"}, To: to, Body: "x"},
				{From: mail.Address{Kind: mail.AddrAgent, ID: "x"}, To: to, Corr: "made-up", Body: "x"},
			} {
				if err := h.Deliver(ctx, env); !errors.Is(err, ErrNotAllowed) {
					t.Fatalf("③ 应被联系名单拒绝：%+v → %v", env.From, err)
				}
			}

			// ④ 跳数超限截断（链上字段经发件箱派发路径带上）。
			if err := h.deliverQueued(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"}, To: to, Hop: MaxHop, Body: "x"}); !errors.Is(err, ErrTooManyHops) {
				t.Fatalf("④ 跳数超限应拒：%v", err)
			}

			// ⑤ 两个并发 agent: 投递带同一 key 只建一个会话。
			errs := make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func(i int) {
					errs <- h.Deliver(ctx, mail.Envelope{ID: fmt.Sprintf("e%d", i), From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
						To: mail.Address{Kind: mail.AddrAgent, ID: "worker"}, Key: "k1", Body: "hi"})
				}(i)
			}
			for i := 0; i < 2; i++ {
				if err := <-errs; err != nil {
					t.Fatalf("⑤ 并发投递应都成功：%v", err)
				}
			}
			if _, err := db.FindSessionByKey(ctx, "worker", "u1", "k1"); err != nil {
				t.Fatalf("⑤ 应建出会话：%v", err)
			}
			list, _ := db.ListSessions(ctx, 0, 100, false)
			if n := len(list); n != 4 { // low、high、s1 + 一个 worker 会话
				t.Fatalf("⑤ 同一 key 应只建一个会话，会话总数 %d（期望 4）", n)
			}
		})
	}
}
