package tenon_test

// T4f 验收（08 8.3）：多人会话里，本轮触发者的用户层权限上限生效——
// 低权限成员的消息触发不了高权限工具；同一会话由高权限成员触发时可以用。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/tenontest"
)

// 用户层把 permission 收窄成 read-only 的触发者，调 write 类工具应被拦。
func TestTriggerUserLimitNarrowsTools(t *testing.T) {
	dir := t.TempDir()
	dd := filepath.Join(dir, "platform", "worker")
	if err := os.MkdirAll(dd, 0o755); err != nil {
		t.Fatal(err)
	}
	// 定义给 full 档位（自己不管权限），权限由用户层决定。
	man := "format = \"tenon/v1\"\nid = \"worker\"\ndescription = \"干活\"\n\n[use.permission]\nmode = \"full\"\n"
	if err := os.WriteFile(filepath.Join(dd, "agent.toml"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	// 用户 u-low 的权限上限是 read-only；u-high 不限制。
	src := memSource{user: map[string]map[string]any{
		"permission": {"mode": "read-only"},
	}}
	app := tenontest.New(t,
		tenon.WithAgents(tenon.Dir(dir)),
		tenon.WithConfigSource(limitedSource{full: src, lowUser: "u-low"}),
		tenontest.Model(
			tenontest.Reply("写").CallTool("write_file", `{"path":"a.txt","content":"x"}`),
			tenontest.Reply("完"),
			tenontest.Reply("写").CallTool("write_file", `{"path":"b.txt","content":"y"}`),
			tenontest.Reply("完"),
		))
	ctx := context.Background()
	host := app.Internals().Host
	// 低权限用户 u-low 触发一轮：write_file 应被拦（不产生成功的工具结果）。
	if err := host.Deliver(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u-low"},
		To:   mail.Address{Kind: mail.AddrAgent, ID: "worker"},
		Kind: mail.KindRequest, Mode: mail.ModeWake, Origin: mail.OriginHuman, Body: "写文件",
	}); err != nil {
		t.Fatal(err)
	}
	// 等这一轮跑完，检查没有成功的 write_file 结果。
	waitTurns(t, app, 1)
	if anyToolOK(t, app, "write_file") {
		t.Fatal("低权限触发者的消息不该能成功调用 write_file（T4f 按本轮触发者求交）")
	}
}

// limitedSource 只对 lowUser 返回用户层配置，其他用户返回空。
type limitedSource struct {
	full    memSource
	lowUser string
}

func (l limitedSource) Tenant(ctx context.Context, tenant, component string) (map[string]any, error) {
	return l.full.Tenant(ctx, tenant, component)
}

func (l limitedSource) User(ctx context.Context, tenant, user, component string) (map[string]any, error) {
	if user == l.lowUser {
		return l.full.User(ctx, tenant, user, component)
	}
	return nil, nil
}

// waitToolResults 等会话历史里出现至少 n 条工具结果（比等 assistant 更稳：工具执行完才算）。
func waitToolResults(t *testing.T, app *tenontest.App, n int) {
	t.Helper()
	db := app.Internals().DB
	for i := 0; i < 600; i++ {
		infos, _ := db.ListSessions(context.Background(), 0, 50, true)
		for _, si := range infos {
			h, _ := app.History(context.Background(), si.ID)
			c := 0
			for _, e := range h {
				if e.Item.ToolResult != nil {
					c++
				}
			}
			if c >= n {
				return
			}
		}
		sleepMs(5)
	}
	t.Fatal("等待工具结果超时")
}

// waitTurns 等会话历史里出现至少 n 条 assistant 条目。
func waitTurns(t *testing.T, app *tenontest.App, n int) {
	t.Helper()
	db := app.Internals().DB
	for i := 0; i < 600; i++ {
		infos, _ := db.ListSessions(context.Background(), 0, 50, true)
		for _, si := range infos {
			h, _ := app.History(context.Background(), si.ID)
			c := 0
			for _, e := range h {
				if e.Item.Kind == "assistant" {
					c++
				}
			}
			if c >= n {
				return
			}
		}
		sleepMs(5)
	}
	t.Fatal("等待回合超时")
}

// anyToolOK 是否有成功的某工具结果。
func anyToolOK(t *testing.T, app *tenontest.App, name string) bool {
	t.Helper()
	db := app.Internals().DB
	infos, _ := db.ListSessions(context.Background(), 0, 50, true)
	for _, si := range infos {
		h, _ := app.History(context.Background(), si.ID)
		for _, e := range h {
			if e.Item.ToolResult != nil && e.Item.ToolResult.Status.State == "ok" {
				return true
			}
		}
	}
	return false
}

func sleepMs(n int) { time.Sleep(time.Duration(n) * time.Millisecond) }

// 对照：同一会话由**不受限**用户触发时，write_file 能成功——证明差异来自"本轮触发者"。
func TestTriggerUserLimitAllowsForHighUser(t *testing.T) {
	dir := t.TempDir()
	dd := filepath.Join(dir, "platform", "worker")
	if err := os.MkdirAll(dd, 0o755); err != nil {
		t.Fatal(err)
	}
	man := "format = \"tenon/v1\"\nid = \"worker\"\ndescription = \"干活\"\n\n[use.permission]\nmode = \"full\"\n"
	if err := os.WriteFile(filepath.Join(dd, "agent.toml"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	src := memSource{user: map[string]map[string]any{"permission": {"mode": "read-only"}}}
	app := tenontest.New(t,
		tenon.WithAgents(tenon.Dir(dir)),
		tenon.WithConfigSource(limitedSource{full: src, lowUser: "u-low"}),
		tenontest.Model(
			tenontest.Reply("写").CallTool("write_file", `{"path":"a.txt","content":"x"}`),
			tenontest.Reply("完"),
		))
	// u-high 不在受限名单里：应能写。
	if err := app.Internals().Host.Deliver(context.Background(), mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u-high"},
		To:   mail.Address{Kind: mail.AddrAgent, ID: "worker"},
		Kind: mail.KindRequest, Mode: mail.ModeWake, Origin: mail.OriginHuman, Body: "写文件",
	}); err != nil {
		t.Fatal(err)
	}
	waitToolResults(t, app, 1)
	if !anyToolOK(t, app, "write_file") {
		t.Fatal("不受限用户触发时 write_file 应能成功（对照）")
	}
}
