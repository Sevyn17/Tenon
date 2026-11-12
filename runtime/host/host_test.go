package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/sqlite"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// fakeModel：最后一条是 user → 若含 "tool" 调 echo，否则回 "re:<文本>"；最后一条是 tool → 回 "done"。
// block 非空时阻塞到 ctx 取消（测试取消）。
type fakeModel struct {
	mu    sync.Mutex
	calls int
	block chan struct{}
	reqs  []session.Request // 回归快照用：记下每次调模型的请求
	// usage 非零时每次模型回复带上它（预算记账测试用；零值不改变既有行为）。
	usage session.Usage
}

func (m *fakeModel) Stream(ctx context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	m.mu.Lock()
	m.calls++
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()
	if m.block != nil {
		select {
		case <-ctx.Done():
			return session.AssistantItem{}, session.StopReason{}, ctx.Err()
		case <-m.block:
		}
	}
	last := req.Messages[len(req.Messages)-1]
	txt := ""
	for _, p := range last.Content {
		txt += p.Text
	}
	m.mu.Lock()
	u := m.usage
	m.mu.Unlock()
	if last.Role == "tool" {
		return session.AssistantItem{Content: text("done"), Usage: u}, session.StopReason{V: "stop"}, nil
	}
	if strings.Contains(txt, "tool") {
		return session.AssistantItem{ToolCalls: []session.ToolCall{{Name: "echo", Args: json.RawMessage(`{"s":"x"}`)}}, Usage: u},
			session.StopReason{V: "tool_calls"}, nil
	}
	return session.AssistantItem{Content: text("re:" + txt), Usage: u}, session.StopReason{V: "stop"}, nil
}

type rig struct {
	h    *Host
	db   *sqlite.DB
	m    *fakeModel
	runs *int
	path string
}

func newRig(t *testing.T, path string, m *fakeModel) *rig {
	t.Helper()
	db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	runs := new(int)
	r := &rig{db: db, m: m, runs: runs, path: path}
	r.h = &Host{DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		echo := &toolhub.Tool{Spec: session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
			Risk: toolhub.Dangerous, Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
				*runs++
				return toolhub.Text("echoed"), nil
			}}
		if err := hub.Register(echo); err != nil {
			return nil, nil, err
		}
		hub.AddGate(askGate())
		a := &agent.Agent{Store: db, SessionID: id, Model: m, Hub: hub}
		cfg := &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}
		return a, cfg, nil
	}}
	return r
}

func messages(t *testing.T, hd Handle) []session.Message {
	t.Helper()
	return hd.Agent().CommittedHistory()
}

func TestSendIdempotentAndNoteMerge(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd, err := r.h.Open(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Do(Command{Kind: Note, ItemID: "n1", Text: "后台任务完成"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // 同 id 重放
		if err := hd.Do(Command{Kind: Send, ItemID: "i1", Text: "你好"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	ms := messages(t, hd)
	if len(ms) != 2 || r.m.calls != 1 {
		t.Fatalf("重放应只开一个回合: msgs=%d calls=%d", len(ms), r.m.calls)
	}
	u := ""
	for _, p := range ms[0].Content {
		u += p.Text
	}
	if !strings.HasPrefix(u, "[补报] 后台任务完成") || !strings.HasSuffix(u, "你好") {
		t.Fatalf("note 应拼在输入前: %q", u)
	}
	if left, _ := r.db.Inbox(context.Background(), "s1"); len(left) != 0 {
		t.Fatalf("inbox 应已消费: %d", len(left))
	}
}

func TestCancelMidTurn(t *testing.T) {
	m := &fakeModel{block: make(chan struct{})}
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), m)
	hd, _ := r.h.Open(context.Background(), "s1")
	var end session.EndReason
	r.h.Hooks.AfterTurn = func(_ string, e session.EndReason) { end = e }
	_ = hd.Do(Command{Kind: Send, Text: "慢"})
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.calls == 1 })
	if err := hd.Do(Command{Kind: Cancel}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if end.Kind != session.EndCancelled || hd.Agent().Committed().Header.Status != session.StatusIdle {
		t.Fatalf("应以 Cancelled 结束并回 Idle: %s", end)
	}
}

func TestConfirmCommandAndCrashResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	r := newRig(t, path, &fakeModel{})
	hd, _ := r.h.Open(context.Background(), "s1")
	if err := hd.Do(Command{Kind: Confirm, CallID: "nope", Approve: true}); err == nil {
		t.Fatal("未等待确认的调用应报错")
	}
	_ = hd.Do(Command{Kind: Send, Text: "用 tool"})
	callID := waitConfirm(t, r.db, "s1")
	if *r.runs != 0 {
		t.Fatal("确认前不得执行")
	}
	// 模拟进程被杀：直接关库，换一个宿主打开同一会话。
	r.db.Close()
	r2 := newRig(t, path, &fakeModel{})
	hd2, err := r2.h.Open(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	id2 := waitConfirm(t, r2.db, "s1")
	if id2 != callID {
		t.Fatalf("恢复后应仍在等同一调用的确认: %s vs %s", id2, callID)
	}
	waitFor(t, func() bool {
		return hd2.Do(Command{Kind: Confirm, CallID: callID, Approve: true}) == nil
	})
	if err := hd2.Wait(); err != nil {
		t.Fatal(err)
	}
	if *r2.runs != 1 {
		t.Fatalf("批准后应执行一次: %d", *r2.runs)
	}
	ms := messages(t, hd2)
	if last := ms[len(ms)-1]; last.Role != "assistant" || last.Content[0].Text != "done" {
		t.Fatalf("回合应完成: %+v", last)
	}
	_ = hd.Done()
}

func TestBeforeTurnDenyAndRecord(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	r.h.Hooks.BeforeTurn = func(_ string, in session.Content, _ bool) (session.Content, Decision, error) {
		if strings.Contains(in[0].Text, "拒") {
			return in, Deny, nil
		}
		return in, RecordOnly, nil
	}
	hd, _ := r.h.Open(context.Background(), "s1")
	_ = hd.Do(Command{Kind: Send, Text: "拒绝我"})
	_ = hd.Do(Command{Kind: Send, Text: "只记录"})
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	ms := messages(t, hd)
	if r.m.calls != 0 || len(ms) != 1 || ms[0].Content[0].Text != "只记录" {
		t.Fatalf("Deny 不写条目、Record 只记录: calls=%d msgs=%+v", r.m.calls, ms)
	}
}

func TestCreateHookRollback(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	r.h.Hooks.OnCreate = func(string, Origin) error { return context.Canceled }
	if _, err := r.h.Open(context.Background(), "s1"); err == nil {
		t.Fatal("create 钩子失败应报错")
	}
	if ok, _ := r.db.Exists(context.Background(), "s1"); ok {
		t.Fatal("应回滚删除会话")
	}
}

func waitConfirm(t *testing.T, db *sqlite.DB, sid string) string {
	t.Helper()
	var id string
	waitFor(t, func() bool {
		ps, _ := db.PendingTools(context.Background(), sid)
		if len(ps) == 1 {
			id = ps[0].CallID
			return true
		}
		return false
	})
	return id
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	dl := time.Now().Add(5 * time.Second)
	for time.Now().Before(dl) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

// M4.5：fork 复制前缀、重分配 id、记 origin；分支各走各的，不影响来源。
func TestForkAndSide(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	ctx := context.Background()
	src, err := r.h.Open(ctx, "src")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"a", "b"} {
		if err := src.Do(Command{Kind: Send, Text: s}); err != nil {
			t.Fatal(err)
		}
		if err := src.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.h.Fork(ctx, "src", "bad", 1, "fork"); err == nil {
		t.Fatal("非回合起点应拒绝")
	}
	f, err := r.h.Fork(ctx, "src", "f1", 2, "fork")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if got := len(messages(t, f)); got != 2 {
		t.Fatalf("fork 应带 2 条前缀消息，得 %d", got)
	}
	srcEntries := src.Agent().Committed().Entries
	fEntries := f.Agent().Committed().Entries
	if srcEntries[0].ID == fEntries[0].ID {
		t.Fatal("fork 条目 id 应重新分配")
	}
	if err := f.Do(Command{Kind: Send, Text: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Wait(); err != nil {
		t.Fatal(err)
	}
	if len(messages(t, f)) != 4 || len(messages(t, src)) != 4 {
		t.Fatalf("分支独立：fork=%d src=%d", len(messages(t, f)), len(messages(t, src)))
	}
	var origin, parent string
	var upto int
	r.db.DB().QueryRow(`SELECT origin, parent_id, fork_upto FROM sessions WHERE id='f1'`).Scan(&origin, &parent, &upto)
	if origin != "fork" || parent != "src" || upto != 2 {
		t.Fatalf("会话元数据: %s %s %d", origin, parent, upto)
	}
	if _, err := r.h.Fork(ctx, "src", "s1", 4, "side"); err != nil {
		t.Fatalf("旁支: %v", err)
	}
	_ = time.Second
}

// askLayer 测试用的“每次询问” Guard：只读放行，其余未批准即要确认（内核测试不依赖权限插件）。
func askGate() toolhub.Gate {
	// 04 4.3.2：纯判定（只读放行、其余要确认），所以是 Gate 不是 Transform。
	return toolhub.Gate{Name: "permission",
		Check: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage) (toolhub.Verdict, string) {
			if cx.Approved || (cx.Tool != nil && cx.Tool.Risk == toolhub.Read) {
				return toolhub.VerdictAllow, ""
			}
			return toolhub.VerdictConfirm, cx.Tool.Spec.Name + " 需要确认"
		}}
}
