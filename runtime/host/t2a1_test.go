package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/modules/storage/sqlite"
	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// scriptedModel T2a-1 测试用模型：记录调用、可并发计数、可阻塞在 gate、可按次序回工具调用。
type scriptedModel struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	reqs      []session.Request
	start     chan int      // 每次调用开始（缓冲由测试给足）
	gate      chan struct{} // 非 nil：每次调用阻塞到关闭
	firstTool bool          // 第一次调用回 echo 工具调用
}

func (m *scriptedModel) Stream(ctx context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.active++
	if m.active > m.maxActive {
		m.maxActive = m.active
	}
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()
	if m.start != nil {
		m.start <- n
	}
	if m.gate != nil {
		select {
		case <-m.gate:
		case <-ctx.Done():
			m.leave()
			return session.AssistantItem{}, session.StopReason{}, ctx.Err()
		}
	}
	m.leave()
	if m.firstTool && n == 1 {
		return session.AssistantItem{ToolCalls: []session.ToolCall{{Name: "echo", Args: json.RawMessage(`{"s":"x"}`)}}},
			session.StopReason{V: "tool_calls"}, nil
	}
	return session.AssistantItem{Content: session.Content{session.NewTextPart("ok")}}, session.StopReason{V: "stop"}, nil
}

func (m *scriptedModel) leave() {
	m.mu.Lock()
	m.active--
	m.mu.Unlock()
}

func (m *scriptedModel) request(i int) session.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reqs[i]
}

func (m *scriptedModel) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// t2Rig 是 T2a-1 的迷你装配：sqlite + 一个可替换的模型 + echo 工具。
type t2Rig struct {
	h    *Host
	db   *sqlite.DB
	runs *int
}

func newT2Rig(t *testing.T, path string, m agent.Model, limit int, call func(context.Context, json.RawMessage) (toolhub.Output, error)) *t2Rig {
	t.Helper()
	db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	runs := new(int)
	if call == nil {
		call = func(context.Context, json.RawMessage) (toolhub.Output, error) {
			*runs++
			return toolhub.Text("echoed"), nil
		}
	}
	r := &t2Rig{db: db, runs: runs}
	r.h = &Host{DB: db, Limit: limit, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		echo := &toolhub.Tool{Spec: session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
			Risk: toolhub.Read, Call: func(ctx context.Context, _ *toolhub.Cx, args json.RawMessage) (toolhub.Output, error) {
				return call(ctx, args)
			}}
		if err := hub.Register(echo); err != nil {
			return nil, nil, err
		}
		a := &agent.Agent{Store: db, SessionID: id, Model: m, Hub: hub}
		cfg := &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil)), Policy: session.DefaultPolicy()}
		return a, cfg, nil
	}}
	return r
}

func mustOpen(t *testing.T, h *Host, id string) Handle {
	t.Helper()
	hd, err := h.Open(context.Background(), id)
	if err != nil {
		t.Fatalf("打开 %s: %v", id, err)
	}
	return hd
}

func waitStarted(t *testing.T, ch chan int, why string) int {
	t.Helper()
	select {
	case n := <-ch:
		return n
	case <-time.After(5 * time.Second):
		t.Fatalf("等待模型开始超时：%s", why)
		return 0
	}
}

func joinParts(c session.Content) string {
	var b strings.Builder
	for _, p := range c {
		b.WriteString(p.Text)
	}
	return b.String()
}

// 09 9.4 ①：同一智能体的多个会话同时推理，互不等待。
func TestConcurrentSessionsInfer(t *testing.T) {
	m := &scriptedModel{start: make(chan int, 2), gate: make(chan struct{})}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, nil)
	a, b := mustOpen(t, r.h, "s1"), mustOpen(t, r.h, "s2")
	if err := a.Do(Command{Kind: Send, Text: "甲"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Do(Command{Kind: Send, Text: "乙"}); err != nil {
		t.Fatal(err)
	}
	waitStarted(t, m.start, "s1")
	waitStarted(t, m.start, "s2")
	close(m.gate)
	if err := a.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := b.Wait(); err != nil {
		t.Fatal(err)
	}
	if m.maxActive < 2 {
		t.Fatalf("两个会话没有同时推理：maxActive=%d", m.maxActive)
	}
}

// 09 9.4 ③：并发上限生效，超出上限的会话排队等待，不报错。
func TestConcurrencyLimitQueues(t *testing.T) {
	m := &scriptedModel{start: make(chan int, 4), gate: make(chan struct{})}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 1, nil)
	a, b := mustOpen(t, r.h, "s1"), mustOpen(t, r.h, "s2")
	if err := a.Do(Command{Kind: Send, Text: "一"}); err != nil {
		t.Fatal(err)
	}
	waitStarted(t, m.start, "第一个会话")
	if err := b.Do(Command{Kind: Send, Text: "二"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := m.getCalls(); n != 1 {
		t.Fatalf("上限=1 时第二个会话不应进入模型，calls=%d", n)
	}
	select {
	case <-m.start:
		t.Fatal("超过上限的会话仍在推理")
	default:
	}
	close(m.gate)
	if err := a.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := b.Wait(); err != nil {
		t.Fatal(err)
	}
	if n := m.getCalls(); n != 2 {
		t.Fatalf("排队后应各跑一次，calls=%d", n)
	}
}

// C3：排队等并发槽位时到达的 Cancel 要生效（旧实现：取消函数在拿到槽位后才登记，Cancel 落空，回合照跑）。
func TestCancelWhileQueuedForSlot(t *testing.T) {
	m := &scriptedModel{start: make(chan int, 4), gate: make(chan struct{})}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 1, nil)
	a, b := mustOpen(t, r.h, "s1"), mustOpen(t, r.h, "s2")
	if err := a.Do(Command{Kind: Send, Text: "一"}); err != nil {
		t.Fatal(err)
	}
	waitStarted(t, m.start, "第一个会话")
	if err := b.Do(Command{Kind: Send, Text: "二"}); err != nil {
		t.Fatal(err)
	}
	// 等 s2 真的进了回合、正在排槽位（turn 先登记取消函数、置 inTurn，再排槽位）：原先固定睡 100ms，
	// 负载高时 s2 还没进回合，Cancel 到达时没有可取消的回合、落空，之后 s2 照常跑了一轮
	// （2026-11-07：CI 全量并行下偶发 calls=2）。
	waitFor(t, b.InTurn)
	if err := b.Do(Command{Kind: Cancel}); err != nil {
		t.Fatal(err)
	}
	close(m.gate)
	if err := a.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := b.Wait(); err != nil {
		t.Fatalf("排队中被取消的会话应正常空闲: %v", err)
	}
	if n := m.getCalls(); n != 1 {
		t.Fatalf("被取消的 s2 不应调模型，calls=%d", n)
	}
	if err := b.Do(Command{Kind: Send, Text: "三"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Wait(); err != nil {
		t.Fatal(err)
	}
	if n := m.getCalls(); n != 2 {
		t.Fatalf("取消后会话应可继续，calls=%d", n)
	}
}

// 09 9.4 ②：运行中到达的用户消息排队，在下一次调模型前插入当前回合。
func TestMidTurnUserMessageInserted(t *testing.T) {
	m := &scriptedModel{firstTool: true}
	toolStarted := make(chan struct{}, 1)
	toolRelease := make(chan struct{})
	call := func(ctx context.Context, _ json.RawMessage) (toolhub.Output, error) {
		toolStarted <- struct{}{}
		select {
		case <-toolRelease:
		case <-ctx.Done():
			return toolhub.Output{}, ctx.Err()
		}
		return toolhub.Text("echoed"), nil
	}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, call)
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, Text: "开始任务"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-toolStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("等待工具执行超时")
	}
	if err := hd.Do(Command{Kind: Send, Text: "补充要求"}); err != nil {
		t.Fatal(err)
	}
	close(toolRelease)
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if n := m.getCalls(); n != 2 {
		t.Fatalf("插入消息不应多开回合：calls=%d", n)
	}
	last := m.request(m.getCalls() - 1)
	all := ""
	for _, msg := range last.Messages {
		all += joinParts(msg.Content) + "\n"
	}
	if !strings.Contains(all, "补充要求") {
		t.Fatalf("下一次模型请求里应含运行中到达的消息：%q", all)
	}
}

// R14：模型正在回工具调用时到达的消息，不能插在调用与结果之间——
// 落到 NeedTools 边界时留在 inbox，等结果到齐后的调模型边界再插。
func TestMidTurnMessageNotBetweenCallAndResult(t *testing.T) {
	m := &scriptedModel{firstTool: true, start: make(chan int, 4), gate: make(chan struct{})}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, nil)
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, Text: "开始任务"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.start: // 第一次调模型已开始（将回工具调用），消息在此期间到达
	case <-time.After(5 * time.Second):
		t.Fatal("等待模型调用超时")
	}
	if err := hd.Do(Command{Kind: Send, Text: "补充要求"}); err != nil {
		t.Fatal(err)
	}
	close(m.gate)
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < m.getCalls(); i++ {
		var roles []string
		for _, msg := range m.request(i).Messages {
			roles = append(roles, msg.Role)
		}
		for j := 1; j < len(roles); j++ {
			if roles[j] == "tool" && roles[j-1] != "assistant" && roles[j-1] != "tool" {
				t.Fatalf("第 %d 次请求里工具结果前不是调用：%v", i+1, roles)
			}
		}
	}
	all := ""
	for _, msg := range m.request(m.getCalls() - 1).Messages {
		all += joinParts(msg.Content) + "\n"
	}
	if !strings.Contains(all, "补充要求") {
		t.Fatalf("消息应在之后的请求里出现：%q", all)
	}
	// 落盘的日志能重新加载（旧行为下 Restore 报「调用结果未闭合就出现新输入」）。
	l, err := r.db.Load(context.Background(), "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Restore(l.Snapshot); err != nil {
		t.Fatalf("落盘日志应能恢复: %v", err)
	}
}

// 工具执行中取消：回合以取消结束，会话仍在（旧行为：用已取消的 ctx 写结果记录失败，顶层会话退出）。
func TestCancelDuringToolKeepsSession(t *testing.T) {
	m := &scriptedModel{firstTool: true}
	started := make(chan struct{}, 1)
	call := func(ctx context.Context, _ json.RawMessage) (toolhub.Output, error) {
		started <- struct{}{}
		<-ctx.Done()
		return toolhub.Text("partial"), nil
	}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, call)
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, Text: "开始任务"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("等待工具执行超时")
	}
	if err := hd.Do(Command{Kind: Cancel}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatalf("取消后回合应正常结束: %v", err)
	}
	select {
	case <-hd.Done():
		t.Fatalf("取消不应让会话退出: %v", hd.Err())
	default:
	}
	if err := hd.Do(Command{Kind: Send, Text: "继续"}); err != nil {
		t.Fatalf("取消后会话应可继续: %v", err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
}

// 09 9.4 ④：租约带纪元——旧持有者的写入被拒绝，接管者可以继续。
func TestLeaseTakeoverRejectsStaleWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	rA := newT2Rig(t, path, &scriptedModel{}, 0, nil)
	hdA := mustOpen(t, rA.h, "s1")
	if err := hdA.Do(Command{Kind: Send, Text: "一"}); err != nil {
		t.Fatal(err)
	}
	if err := hdA.Wait(); err != nil {
		t.Fatal(err)
	}
	// 另一个进程内宿主接管：epoch 加一。
	rB := newT2Rig(t, path, &scriptedModel{}, 0, nil)
	hdB := mustOpen(t, rB.h, "s1")
	// 旧宿主再写：下一个检查点应被存储拒绝。
	if err := hdA.Do(Command{Kind: Send, Text: "二"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hdA.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("旧持有者没有因租约失效退出")
	}
	if err := hdA.Err(); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("旧持有者应报租约失效，得 %v", err)
	}
	// 接管者把排队中的消息继续处理完。
	if err := hdB.Wait(); err != nil {
		t.Fatal(err)
	}
	ms := messages(t, hdB)
	found := false
	for _, msg := range ms {
		if msg.Role == "user" && strings.Contains(joinParts(msg.Content), "二") {
			found = true
		}
	}
	if !found {
		t.Fatalf("接管后应处理排队的消息，历史=%+v", ms)
	}
}
