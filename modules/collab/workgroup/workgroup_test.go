package workgroup

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/sqlite"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// echoModel 回显用户消息；seen 记录请求里出现的每一条用户消息（T2a-1 起运行中到达的
// 用户消息会插入当前回合，两个收件人消息可能落在同一次请求里，只看最后一条会漏）。
type echoModel struct {
	mu   sync.Mutex
	seen []string
}

func (m *echoModel) Stream(ctx context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	last := ""
	var users []string
	for _, msg := range req.Messages {
		if msg.Role != "user" {
			continue
		}
		txt := ""
		for _, p := range msg.Content {
			txt += p.Text
		}
		users = append(users, txt)
		last = txt
	}
	if last == "" { // 没有用户消息（不应发生）：取最后一条
		for _, p := range req.Messages[len(req.Messages)-1].Content {
			last += p.Text
		}
	}
	m.mu.Lock()
	m.seen = append(m.seen, users...)
	m.mu.Unlock()
	return session.AssistantItem{Content: session.Content{session.NewTextPart("re:" + last)}}, session.StopReason{V: "stop"}, nil
}

func (m *echoModel) has(sub string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.seen {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func rig(t *testing.T) (*Group, *host.Host, *sqlite.DB, *echoModel) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := &echoModel{}
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
			return a, &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}, nil
		}
	}
	h.New = mk(true)
	if err := db.SetCollectionVersion(context.Background(), store.Tenant, "workgroup", "tasks", 1); err != nil {
		t.Fatal(err)
	}
	tasks, err := db.Open(context.Background(), store.Tenant, "workgroup", "tasks", store.CollectionSpec{
		Version: 1, Indexes: []string{"status", "owner"}, Partition: "team"})
	if err != nil {
		t.Fatal(err)
	}
	g, err = New(context.Background(), h, db, tasks, db, mk(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g, h, db, m
}

func call(t *testing.T, g *Group, tools []*toolhub.Tool, name, sid, args string) toolhub.Output {
	t.Helper()
	for _, tl := range tools {
		if tl.Spec.Name == name {
			out, err := tl.Call(context.Background(), &toolhub.Cx{SessionID: sid, CallID: "c1"}, json.RawMessage(args))
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatalf("没有工具 %s", name)
	return toolhub.Output{}
}

func txt(o toolhub.Output) string {
	s := ""
	for _, p := range o.Content {
		s += p.Text
	}
	return s
}

func TestSpawnDeliversResultToLeader(t *testing.T) {
	g, h, db, m := rig(t)
	ctx := context.Background()
	if _, err := h.Open(ctx, "L"); err != nil {
		t.Fatal(err)
	}
	out := call(t, g, g.LeaderTools(), "team_spawn", "L", `{"name":"alice","task":"查资料"}`)
	if out.Status != session.ToolOK {
		t.Fatal(txt(out))
	}
	g.Wait()
	l, err := db.Load(ctx, "L/m-alice", nil)
	if err != nil || l.Meta.ParentID != "L" || l.Meta.Origin != "member" {
		t.Fatalf("成员会话 = %+v %v", l, err)
	}
	hd, _ := h.Get("L")
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if !m.has("[成员 alice 完成]\nre:查资料") {
		t.Fatalf("领导没收到结果：%v", m.seen)
	}
	// 成员不能派成员
	if o := call(t, g, g.LeaderTools(), "team_spawn", "L/m-alice", `{"name":"bob","task":"x"}`); o.Status != session.ToolError {
		t.Fatal("成员应不能派成员")
	}
	// 成员给领导发消息
	if o := call(t, g, g.MemberTools(), "team_send", "L/m-alice", `{"to":"lead","message":"进度 50%"}`); o.Status != session.ToolOK {
		t.Fatal(txt(o))
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if !m.has("[来自 alice] 进度 50%") {
		t.Fatalf("领导没收到消息：%v", m.seen)
	}
	// 发给不存在的成员
	if o := call(t, g, g.LeaderTools(), "team_send", "L", `{"to":"nobody","message":"x"}`); o.Status != session.ToolError {
		t.Fatal("应报没有成员")
	}
	// 领导给空闲成员发消息 → 成员开回合，结果回报
	if o := call(t, g, g.LeaderTools(), "team_send", "L", `{"to":"alice","message":"再查一项"}`); o.Status != session.ToolOK {
		t.Fatal(txt(o))
	}
	g.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for !m.has("[成员 alice 完成]\nre:[来自 lead] 再查一项") {
		if time.Now().After(deadline) {
			t.Fatalf("未回报：%v", m.seen)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBoard(t *testing.T) {
	g, _, _, _ := rig(t)
	ctx := context.Background()
	a, err := g.Create(ctx, "L", "写文档", "")
	if err != nil || a.ID != 1 {
		t.Fatal(a, err)
	}
	b, _ := g.Create(ctx, "L", "跑测试", "")
	if b.ID != 2 {
		t.Fatal(b)
	}
	if o, _ := g.Create(ctx, "M", "别组", ""); o.ID != 1 {
		t.Fatal("组间 id 独立", o)
	}
	if _, err := g.Claim(ctx, "L", 1, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Claim(ctx, "L", 1, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatal("别人领过的应冲突", err)
	}
	if _, err := g.Complete(ctx, "L", 1, "bob", "x"); !errors.Is(err, ErrConflict) {
		t.Fatal("非负责人不能完成", err)
	}
	if tk, err := g.Complete(ctx, "L", 1, "alice", "done"); err != nil || tk.Status != "completed" || tk.Rev != 3 {
		t.Fatal(tk, err)
	}
	if _, err := g.Claim(ctx, "L", 1, "alice"); !errors.Is(err, ErrConflict) {
		t.Fatal("已完成不能再领", err)
	}
	// 领导可直接完成未领的任务
	if tk, err := g.Complete(ctx, "L", 2, "lead", "ok"); err != nil || tk.Owner != "lead" {
		t.Fatal(tk, err)
	}
	// 工具面：成员会话看到的是同一看板
	out := call(t, g, g.MemberTools(), "task_list", "L/m-alice", `{}`)
	var ts []Task
	if err := json.Unmarshal([]byte(txt(out)), &ts); err != nil || len(ts) != 2 || ts[0].Result != "done" {
		t.Fatalf("%s %v", txt(out), err)
	}
	if o := call(t, g, g.MemberTools(), "task_claim", "L/m-alice", `{"id":9}`); o.Status != session.ToolError {
		t.Fatal("不存在的任务应报错")
	}
}

// TestImportTasks 旧表 p_workgroup_tasks 在 Start（New）时搬进集合：行进去、表删掉、id 与 rev 保持原值。
func TestImportTasks(t *testing.T) {
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if _, err := db.DB().ExecContext(ctx, `CREATE TABLE p_workgroup_tasks(
		team TEXT NOT NULL, id INTEGER NOT NULL,
		subject TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
		owner TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending',
		result TEXT NOT NULL DEFAULT '', rev INTEGER NOT NULL DEFAULT 1,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
		PRIMARY KEY(team, id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO p_workgroup_tasks
		(team, id, subject, description, owner, status, result, rev, created_at, updated_at) VALUES
		('L', 2, '写文档', '说明', 'alice', 'in_progress', '', 3, 1, 1),
		('L', 5, '跑测试', '', '', 'pending', '', 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.SetCollectionVersion(ctx, store.Tenant, "workgroup", "tasks", 1); err != nil {
		t.Fatal(err)
	}
	tasks, err := db.Open(ctx, store.Tenant, "workgroup", "tasks", store.CollectionSpec{
		Version: 1, Indexes: []string{"status", "owner"}, Partition: "team"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(ctx, &host.Host{DB: db}, db, tasks, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)

	var left int
	err = db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM p_workgroup_tasks`).Scan(&left)
	if err == nil {
		t.Fatalf("旧表应已删除，还剩 %d 行", left)
	}
	if !strings.Contains(err.Error(), "no such table") {
		t.Fatal(err)
	}
	got, err := g.List(ctx, "L")
	if err != nil || len(got) != 2 {
		t.Fatalf("搬迁后 = %+v %v", got, err)
	}
	if got[0].ID != 2 || got[0].Rev != 3 || got[0].Owner != "alice" || got[0].Status != "in_progress" || got[0].Description != "说明" {
		t.Fatalf("第一行 id 与 rev 应保持原值：%+v", got[0])
	}
	if got[1].ID != 5 || got[1].Rev != 1 || got[1].Status != "pending" {
		t.Fatalf("第二行：%+v", got[1])
	}
	// 计数器要抬到已搬的最大 id，否则下一个新建任务会与旧 id 撞车。
	next, err := g.Create(ctx, "L", "新任务", "")
	if err != nil || next.ID != 6 {
		t.Fatalf("搬迁后新建 = %+v %v", next, err)
	}
}

func TestRecoverRedelivers(t *testing.T) {
	g, h, db, m := rig(t)
	ctx := context.Background()
	if _, err := h.Open(ctx, "L"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for _, r := range [][]string{
		{"L/m-a", "a", "item-a", "done", "[成员 a 完成]\nre:旧结果"},
		{"L/m-b", "b", "item-b", "running", ""},
	} {
		if _, err := db.DB().Exec(`INSERT INTO p_workgroup_runs(member, leader, name, item_id, state, msg, updated_at) VALUES(?, 'L', ?, ?, ?, ?, ?)`,
			r[0], r[1], r[2], r[3], r[4], now); err != nil {
			t.Fatal(err)
		}
	}
	n, err := g.Recover(ctx)
	if err != nil || n != 2 {
		t.Fatalf("Recover = %d %v", n, err)
	}
	hd, _ := h.Get("L")
	deadline := time.Now().Add(5 * time.Second)
	for !(m.has("re:旧结果") && m.has("[成员 b 中断]")) {
		if time.Now().After(deadline) {
			t.Fatalf("未补投：%v", m.seen)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if n, err := g.Recover(ctx); err != nil || n != 0 {
		t.Fatalf("二次 Recover = %d %v", n, err)
	}
	// 同 item id 重投被 inbox 拒绝（幂等）
	if err := g.deliverID(ctx, "L", "member:a", "item-a", "dup", false); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if m.has("dup") {
		t.Fatal("重投不应再次进入")
	}
}
