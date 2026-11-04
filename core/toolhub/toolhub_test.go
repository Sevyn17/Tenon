package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ybase-kernel/core/callctx"
	"ybase-kernel/core/session"
)

// —— 假存储 ——

type memStore struct {
	mu   sync.Mutex
	recs map[string]*Record
}

func newMem() *memStore { return &memStore{recs: map[string]*Record{}} }

func (m *memStore) Lookup(_ context.Context, _, id string) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[id]
	if !ok {
		return Record{}, false, nil
	}
	return *r, true, nil
}
func (m *memStore) PutToolStarted(_ context.Context, _, id, h string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.recs[id]; ok {
		if r.Status != "delivered" {
			return errors.New("exists")
		}
		r.Status = "started"
		return nil
	}
	m.recs[id] = &Record{Status: "started", ArgsHash: h}
	return nil
}
func (m *memStore) FinishTool(_ context.Context, _, id, from, out string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.recs[id]
	if from != "" && (r == nil || r.Status != from) {
		return errors.New("state mismatch")
	}
	if from == "" && r != nil { // 与 sqlite 同语义：from="" 是插入，已有记录即冲突
		return errors.New("state mismatch")
	}
	if r == nil {
		r = &Record{}
		m.recs[id] = r
	}
	r.Status, r.Output = "done", out
	return nil
}
func (m *memStore) PutPending(_ context.Context, _, id, h, _ string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.recs[id]; !ok {
		m.recs[id] = &Record{Status: "pending", ArgsHash: h}
	}
	return nil
}
func (m *memStore) decide(id, d string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs[id].Status, m.recs[id].Decision = "delivered", d
}

type memArt struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (a *memArt) Put(_ context.Context, _, _ string, d []byte) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h := ArgsHash(d)
	a.m[h] = d
	return h, nil
}
func (a *memArt) Get(h string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	d, ok := a.m[h]
	if !ok {
		return nil, errors.New("no blob")
	}
	return d, nil
}

func call(id, name, args string) session.ToolCall {
	return session.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
}

func text(o Output) string { return contentText(o.Content) }

func drain(r *Run) []Result {
	var out []Result
	for {
		res, ok := r.Next()
		if !ok {
			return out
		}
		out = append(out, res)
	}
}

func spec(name string) session.ToolSpec {
	return session.ToolSpec{Name: name, Schema: json.RawMessage(`{"type":"object"}`)}
}

// —— 幂等：有副作用的不重复执行；started → 状态未知 ——

func TestIdempotency(t *testing.T) {
	st := newMem()
	h := New(Options{Store: st})
	var runs int32
	_ = h.Register(&Tool{Spec: spec("w"), Risk: Write, Call: func(context.Context, *Cx, json.RawMessage) (Output, error) {
		atomic.AddInt32(&runs, 1)
		return Text("ok"), nil
	}})
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("c1", "w", `{}`)}, nil))
	if text(rs[0].Output) != "ok" || runs != 1 {
		t.Fatalf("首次执行: %+v runs=%d", rs, runs)
	}
	rs = drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("c1", "w", `{}`)}, nil))
	if text(rs[0].Output) != "ok" || runs != 1 {
		t.Fatalf("重放应取回不重跑: %+v runs=%d", rs, runs)
	}
	st.recs["c2"] = &Record{Status: "started"}
	rs = drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("c2", "w", `{}`)}, nil))
	if !strings.Contains(text(rs[0].Output), "结果未知") || runs != 1 {
		t.Fatalf("started 应回状态未知: %q", text(rs[0].Output))
	}
	// 结果未知的记录要结清：从 started 改成 done（存的就是交给模型的那条结果），
	// 再次恢复时按 done 原样返回、仍是错误结果，不再停在 started（2026-11-03 演示）。
	if r := st.recs["c2"]; r.Status != "done" || !strings.Contains(r.Output, "结果未知") {
		t.Fatalf("started 记录恢复后应结清为 done：%+v", r)
	}
	rs = drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("c2", "w", `{}`)}, nil))
	if runs != 1 || rs[0].Output.Status != session.ToolError || !strings.Contains(text(rs[0].Output), "结果未知") {
		t.Fatalf("再次恢复应按 done 原样返回：runs=%d %+v", runs, rs[0].Output)
	}
}

// —— 并行：读并发、写独占、结果按调用顺序；先写后读读到新值 ——

func TestParallelOrderAndExclusive(t *testing.T) {
	h := New(Options{})
	var mu sync.Mutex
	val := "old"
	var inflight, peak int32
	enter := func() {
		n := atomic.AddInt32(&inflight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
	}
	_ = h.Register(&Tool{Spec: spec("r"), Risk: Read, Call: func(context.Context, *Cx, json.RawMessage) (Output, error) {
		enter()
		defer atomic.AddInt32(&inflight, -1)
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return Text(val), nil
	}})
	_ = h.Register(&Tool{Spec: spec("w"), Risk: Write, Call: func(context.Context, *Cx, json.RawMessage) (Output, error) {
		enter()
		defer atomic.AddInt32(&inflight, -1)
		if n := atomic.LoadInt32(&inflight); n != 1 {
			return Errorf("写期间有 %d 个并发", n), nil
		}
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		val = "new"
		mu.Unlock()
		return Text("written"), nil
	}})
	calls := []session.ToolCall{call("1", "r", `{}`), call("2", "r", `{}`), call("3", "w", `{}`), call("4", "r", `{}`)}
	rs := drain(h.Execute(context.Background(), kctx("s"), calls, nil))
	got := []string{}
	for i, r := range rs {
		if r.Call.ID != calls[i].ID {
			t.Fatalf("结果顺序错: %d %s", i, r.Call.ID)
		}
		got = append(got, text(r.Output))
	}
	if strings.Join(got, ",") != "old,old,written,new" {
		t.Fatalf("结果 %v", got)
	}
	if peak < 2 {
		t.Fatalf("两个读应并发，峰值 %d", peak)
	}
}

func TestParallelLimit(t *testing.T) {
	h := New(Options{MaxParallel: 2})
	var inflight, peak int32
	_ = h.Register(&Tool{Spec: spec("r"), Risk: Read, Call: func(context.Context, *Cx, json.RawMessage) (Output, error) {
		n := atomic.AddInt32(&inflight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		return Text("x"), nil
	}})
	var calls []session.ToolCall
	for i := 0; i < 6; i++ {
		calls = append(calls, call(fmt.Sprint(i), "r", `{}`))
	}
	drain(h.Execute(context.Background(), kctx("s"), calls, nil))
	if peak > 2 {
		t.Fatalf("并发上限 2，峰值 %d", peak)
	}
}

// —— 层：Guard 失败即拒、Transform 失败跳过、Transform 不能返回 Confirm ——

func TestLayerFailurePolicies(t *testing.T) {
	h := New(Options{})
	_ = h.Register(&Tool{Spec: spec("r"), Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { return Text("ran"), nil }})
	h.AddLayer(Layer{Name: "t-broken", FailOpen: true, Call: func(context.Context, *Cx, json.RawMessage, Next) (Output, error) {
		return Output{}, errors.New("boom")
	}})
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "r", `{}`)}, nil))
	if text(rs[0].Output) != "ran" {
		t.Fatalf("Transform FailOpen 应跳过: %q", text(rs[0].Output))
	}

	// Transform FailOpen 调过 next 之后才 panic / 超时：工具只执行一次，结果用工具本身的
	// （审查 P0-8：原先再调一次 inner，写工具被执行两次）。
	for _, mode := range []string{"panic", "timeout"} {
		runs := 0
		hf := New(Options{})
		_ = hf.Register(&Tool{Spec: spec("w"), Risk: Write, Call: func(context.Context, *Cx, json.RawMessage) (Output, error) {
			runs++
			return Text("wrote"), nil
		}})
		hf.AddLayer(Layer{Name: "t-late", FailOpen: true, Timeout: 10 * time.Millisecond,
			Call: func(ctx context.Context, cx *Cx, args json.RawMessage, next Next) (Output, error) {
				out, err := next(ctx, cx, args)
				if mode == "panic" {
					panic("after next")
				}
				time.Sleep(30 * time.Millisecond)
				return out, err
			}})
		rs := drain(hf.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "w", `{}`)}, nil))
		if runs != 1 || text(rs[0].Output) != "wrote" {
			t.Fatalf("%s：工具应只执行一次并返回其结果: runs=%d out=%q", mode, runs, text(rs[0].Output))
		}
	}

	h2 := New(Options{})
	_ = h2.Register(&Tool{Spec: spec("r"), Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { return Text("ran"), nil }})
	h2.AddLayer(Layer{Name: "perm", Guard: true, Call: func(context.Context, *Cx, json.RawMessage, Next) (Output, error) {
		panic("guard crash")
	}})
	rs = drain(h2.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "r", `{}`)}, nil))
	if !strings.Contains(text(rs[0].Output), "拒绝") || rs[0].Output.Status != session.ToolError {
		t.Fatalf("Guard panic 应拒绝: %q", text(rs[0].Output))
	}

	h3 := New(Options{})
	_ = h3.Register(&Tool{Spec: spec("r"), Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { return Text("ran"), nil }})
	h3.AddLayer(Layer{Name: "sneaky", Call: func(context.Context, *Cx, json.RawMessage, Next) (Output, error) {
		return Output{Kind: Confirm, Reason: "x"}, nil
	}})
	rs = drain(h3.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "r", `{}`)}, nil))
	if !strings.Contains(text(rs[0].Output), "只能由 Guard") {
		t.Fatalf("Transform 返回 Confirm 应被拦: %q", text(rs[0].Output))
	}
}

// 层超时只计本层自身耗时：next 慢不算本层超时。
func TestLayerTimeoutExcludesInner(t *testing.T) {
	h := New(Options{})
	_ = h.Register(&Tool{Spec: spec("slow"), Call: func(context.Context, *Cx, json.RawMessage) (Output, error) {
		time.Sleep(80 * time.Millisecond)
		return Text("done"), nil
	}})
	h.AddLayer(Layer{Name: "g", Guard: true, Timeout: 30 * time.Millisecond,
		Call: func(ctx context.Context, cx *Cx, a json.RawMessage, next Next) (Output, error) {
			return next(ctx, cx, a)
		}})
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "slow", `{}`)}, nil))
	if text(rs[0].Output) != "done" {
		t.Fatalf("内层耗时不应计入本层: %q", text(rs[0].Output))
	}
}

// —— Schema 校验、提示短路、未知工具 ——

func TestEntryChecks(t *testing.T) {
	h := New(Options{})
	ran := false
	_ = h.Register(&Tool{Spec: session.ToolSpec{Name: "need", Schema: json.RawMessage(`{"type":"object","required":["path"]}`)},
		Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { ran = true; return Text("x"), nil }})
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{
		call("1", "need", `{}`), call("2", "need", `[1]`), call("3", "nope", `{}`), call("4", "need", `{"path":"a"}`),
	}, map[string]string{"4": "单步调用过多"}))
	if !strings.Contains(text(rs[0].Output), "path") || !strings.Contains(text(rs[1].Output), "JSON 对象") ||
		!strings.Contains(text(rs[2].Output), "不存在") || text(rs[3].Output) != "单步调用过多" || ran {
		t.Fatalf("入口校验: %q %q %q %q ran=%v", text(rs[0].Output), text(rs[1].Output), text(rs[2].Output), text(rs[3].Output), ran)
	}
}

// —— 存工件与翻页，预览方向 ——

func TestSpillAndReadArtifact(t *testing.T) {
	art := &memArt{m: map[string][]byte{}}
	th := Thresholds{Bytes: 1 << 20, Lines: 10, Preview: 40, PageLines: 5}
	h := New(Options{Artifacts: art, Thresholds: &th})
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	big := strings.Join(lines, "\n")
	_ = h.Register(&Tool{Spec: spec("sh"), Attrs: map[string]any{AttrPreview: "tail"},
		Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { return Text(big), nil }})
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "sh", `{}`)}, nil))
	out := text(rs[0].Output)
	if !strings.Contains(out, ArtifactPrefix) || !strings.Contains(out, "line-29") || strings.Contains(out, "line-00\n") {
		t.Fatalf("tail 预览 + 引用: %q", out)
	}
	i := strings.Index(out, ArtifactPrefix)
	ref := strings.Fields(out[i:])[0]
	ref = strings.TrimRight(ref, "）)。,，")
	args, _ := json.Marshal(map[string]any{"ref": ref, "offset": 6, "limit": 5}) // 第 6–10 行 = line-05..line-09
	rs = drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{{ID: "2", Name: "read_artifact", Args: args}}, nil))
	pg := text(rs[0].Output)
	if !strings.Contains(pg, "line-05") || strings.Contains(pg, "line-10") {
		t.Fatalf("第 2 页应为 5–9 行: %q", pg)
	}
}

func TestPreviewModes(t *testing.T) {
	s := "abcdefghijklmnopqrstuvwxyz"
	if Preview(s, "head", 5) != "abcde" || !strings.HasSuffix(Preview(s, "tail", 5), "vwxyz") {
		t.Fatalf("head/tail: %q %q", Preview(s, "head", 5), Preview(s, "tail", 5))
	}
	ht := Preview(s, "head_tail", 10)
	if !strings.HasPrefix(ht, "abcde") || !strings.HasSuffix(ht, "vwxyz") || !strings.Contains(ht, "省略") {
		t.Fatalf("head_tail: %q", ht)
	}
	if p := Preview("你好世界你好世界", "head", 4); !strings.HasPrefix("你好世界你好世界", p) {
		t.Fatalf("应按 rune 截断: %q", p)
	}
}

// —— 取消：Read 立即中止；Write 不随取消中止，等 finish_grace 内完成并调用 Cancel ——

func TestCancelSemantics(t *testing.T) {
	h := New(Options{})
	cancelled := make(chan struct{}, 1)
	_ = h.Register(&Tool{Spec: spec("r"), Call: func(ctx context.Context, _ *Cx, _ json.RawMessage) (Output, error) {
		select {
		case <-ctx.Done():
			return Errorf("中止"), nil
		case <-time.After(2 * time.Second):
			return Text("完成"), nil
		}
	}})
	_ = h.Register(&Tool{Spec: spec("w"), Risk: Write,
		Call: func(ctx context.Context, _ *Cx, _ json.RawMessage) (Output, error) {
			select {
			case <-ctx.Done():
				return Errorf("写被中止"), nil
			case <-time.After(150 * time.Millisecond):
				return Text("写完"), nil
			}
		},
		Cancel: func(context.Context, *Cx) { cancelled <- struct{}{} }})

	ctx, cancel := context.WithCancel(context.Background())
	run := h.Execute(ctx, kctx("s"), []session.ToolCall{call("1", "r", `{}`)}, nil)
	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	rs := drain(run)
	if time.Since(start) > time.Second || !strings.Contains(text(rs[0].Output), "中止") {
		t.Fatalf("Read 应立即中止: %q %s", text(rs[0].Output), time.Since(start))
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	run = h.Execute(ctx2, kctx("s"), []session.ToolCall{call("2", "w", `{}`)}, nil)
	time.AfterFunc(20*time.Millisecond, cancel2)
	rs = drain(run)
	if text(rs[0].Output) != "写完" {
		t.Fatalf("Write 应在宽限期内完成，不被中止: %q", text(rs[0].Output))
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("取消时应级联调用 Tool.Cancel")
	}
}

// —— tool span ——

func TestSpanPerCall(t *testing.T) {
	var mu sync.Mutex
	var evs []SpanEvent
	h := New(Options{OnSpan: func(e SpanEvent) { mu.Lock(); evs = append(evs, e); mu.Unlock() }})
	_ = h.Register(&Tool{Spec: spec("r"), Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { return Text("hi"), nil }})
	drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "r", `{}`), call("2", "x", `{}`)}, nil))
	mu.Lock()
	defer mu.Unlock()
	if len(evs) != 2 || evs[0].ResultBytes == 0 || evs[0].End.Before(evs[0].Start) {
		t.Fatalf("每次调用一个 span: %+v", evs)
	}
}

// kctx 测试用调用上下文（框架设计 04 §4.4）。
func kctx(id string) *callctx.Ctx { return callctx.New(callctx.Options{SessionID: id}) }
