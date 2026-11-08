package modelhub

import (
	"context"
	"sync"
	"testing"
	"time"

	"tenon/runtime/session"
)

// fakeAdapter 按脚本逐次返回结果；script[i] 为第 i 次调用（跨模型累计）的行为。
type fakeAdapter struct {
	mu     sync.Mutex
	calls  []string // 记录 ep.Model + "|" + AttemptKey
	script func(n int, ep Endpoint) []Event
	hang   bool                          // 不发任何事件，直到 ctx 取消
	onReq  func(r *Request, ep Endpoint) // 可空：看每次调用收到的请求
}

func (f *fakeAdapter) Protocol() string { return "fake" }

func (f *fakeAdapter) Stream(ctx context.Context, r *Request, ep Endpoint) <-chan Event {
	f.mu.Lock()
	n := len(f.calls)
	f.calls = append(f.calls, ep.Model+"|"+r.AttemptKey)
	if f.onReq != nil {
		f.onReq(r, ep)
	}
	f.mu.Unlock()
	ch := make(chan Event, 16)
	go func() {
		defer close(ch)
		if f.hang {
			<-ctx.Done()
			ch <- Event{Failed: &Failure{Code: CodeAborted}}
			return
		}
		for _, ev := range f.script(n, ep) {
			ch <- ev
		}
	}()
	return ch
}

func done(text string) Event {
	return Event{Done: &Done{Reply: session.AssistantItem{Content: session.Content{session.NewTextPart(text)},
		Usage: session.Usage{Input: 10, Output: 2, CacheRead: 6, Source: "Reported"}}}}
}

func fail(c Code) Event { return Event{Failed: &Failure{Code: c, Message: string(c)}} }

func testHub(t *testing.T, ad *fakeAdapter, catalog string) (*Hub, *[]AttemptSpan) {
	t.Helper()
	f, err := ParseCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	cat := NewCatalog()
	if err := cat.Add(LayerUser, "test", f); err != nil {
		t.Fatal(err)
	}
	pol := DefaultPolicy
	pol.FirstEvent, pol.Idle = 200*time.Millisecond, 200*time.Millisecond
	h := New(cat, func(string) (string, error) { return "k", nil }, &pol)
	h.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() } // 不真等
	if err := h.RegisterAdapter(ad); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	spans := &[]AttemptSpan{}
	h.OnSpan = func(s AttemptSpan) { mu.Lock(); *spans = append(*spans, s); mu.Unlock() }
	return h, spans
}

const twoModels = `
[provider.a]
protocol = "fake"
key = "env:A"
[provider.b]
protocol = "fake"
key = "env:B"
[model."a/m1"]
context_window = 1000
[model."b/m2"]
context_window = 500
[alias.default]
plan = [{ model = "a/m1", retries = 2 }, { model = "b/m2", retries = 1 }]
`

func collect(ch <-chan Event) (deltas []string, resets int, d *Done, f *Failure, terminals int) {
	for ev := range ch {
		switch {
		case ev.Reset:
			resets++
		case ev.Done != nil:
			d = ev.Done
			terminals++
		case ev.Failed != nil:
			f = ev.Failed
			terminals++
		case ev.TextDelta != "":
			deltas = append(deltas, ev.TextDelta)
		}
	}
	return
}

func req(id string) *Request {
	return &Request{Req: session.Request{Model: "default"}, RequestID: id}
}

func TestRetryThenSuccessWithReset(t *testing.T) {
	ad := &fakeAdapter{script: func(n int, _ Endpoint) []Event {
		if n == 0 {
			return []Event{{TextDelta: "半截"}, fail(CodeServer)}
		}
		return []Event{{TextDelta: "好"}, done("好")}
	}}
	h, spans := testHub(t, ad, twoModels)
	deltas, resets, d, f, terms := collect(h.Call(context.Background(), req("need-1")))
	if f != nil || d == nil || terms != 1 {
		t.Fatalf("应成功且恰好一个终止事件: d=%v f=%v terms=%d", d, f, terms)
	}
	if resets != 1 || len(deltas) != 2 {
		t.Fatalf("重试前应发 Reset：resets=%d deltas=%v", resets, deltas)
	}
	if ad.calls[0] != "m1|need-1#0" || ad.calls[1] != "m1|need-1#0" {
		t.Fatalf("同项重试应沿用 attempt key: %v", ad.calls)
	}
	if len(*spans) != 2 || (*spans)[1].Usage.CacheRead != 6 || (*spans)[0].Code != CodeServer || (*spans)[1].ShellHash == "" {
		t.Fatalf("attempt span 不符: %+v", *spans)
	}
}

func TestFallbackChangesAttemptKey(t *testing.T) {
	ad := &fakeAdapter{script: func(n int, ep Endpoint) []Event {
		if ep.Model == "m1" {
			return []Event{fail(CodeAuth)}
		}
		return []Event{done("备用")}
	}}
	h, _ := testHub(t, ad, twoModels)
	_, _, d, f, _ := collect(h.Call(context.Background(), req("need-2")))
	if f != nil || d == nil {
		t.Fatalf("Auth 应换下一项并成功: %v", f)
	}
	if len(ad.calls) != 2 || ad.calls[1] != "m2|need-2#1" {
		t.Fatalf("换项应换 attempt key、Auth 不重试: %v", ad.calls)
	}
}

func TestContextOverflowPassesThrough(t *testing.T) {
	ad := &fakeAdapter{script: func(int, Endpoint) []Event { return []Event{fail(CodeContextOverflow)} }}
	h, _ := testHub(t, ad, twoModels)
	_, _, _, f, _ := collect(h.Call(context.Background(), req("n")))
	if f == nil || f.Code != CodeContextOverflow || len(ad.calls) != 1 {
		t.Fatalf("ContextOverflow 应原样上抛、不重试不换项: %v %v", f, ad.calls)
	}
}

func TestRetryAfterBoundAndExhaust(t *testing.T) {
	var waits []time.Duration
	ad := &fakeAdapter{script: func(n int, ep Endpoint) []Event {
		if ep.Model == "m1" && n == 0 {
			return []Event{{Failed: &Failure{Code: CodeRateLimit, RetryAfter: 3 * time.Second}}}
		}
		if ep.Model == "m1" {
			return []Event{{Failed: &Failure{Code: CodeRateLimit, RetryAfter: time.Hour}}}
		}
		return []Event{done("ok")}
	}}
	h, _ := testHub(t, ad, twoModels)
	h.sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	_, _, d, _, _ := collect(h.Call(context.Background(), req("n")))
	if d == nil {
		t.Fatal("超上限的 Retry-After 应换项后成功")
	}
	if len(waits) != 1 || waits[0] != 3*time.Second {
		t.Fatalf("应遵守边界内的 Retry-After: %v", waits)
	}
}

// 熔断按请求计数（C9）：BreakAfter 个请求在同一模型上最终失败后打开，之后的请求直接跳过它。
func TestCircuitOpensAndSkips(t *testing.T) {
	ad := &fakeAdapter{script: func(_ int, ep Endpoint) []Event {
		if ep.Model == "m1" {
			return []Event{fail(CodeServer)}
		}
		return []Event{done("ok")}
	}}
	h, _ := testHub(t, ad, twoModels)
	h.pol.BreakAfter = 3
	for i := 0; i < 3; i++ { // 3 个请求在 m1 上最终失败（每个请求内部重试 3 次，只记一次失败）
		collect(h.Call(context.Background(), req("n")))
	}
	before := len(ad.calls)
	collect(h.Call(context.Background(), req("n")))
	if got := ad.calls[before:]; len(got) != 1 || got[0][:2] != "m2" {
		t.Fatalf("熔断打开后应直接跳过 m1: %v", got)
	}
}

// C9：单个请求自己的重试不会打开熔断，重试次数也不会被熔断截断
// （旧实现：BreakAfter = Retries 时，第 N 次失败就打开熔断、跳过最后一次重试，并殃及别的请求）。
func TestOneRequestRetriesDoNotOpenCircuit(t *testing.T) {
	ad := &fakeAdapter{script: func(_ int, ep Endpoint) []Event {
		if ep.Model == "m1" {
			return []Event{fail(CodeServer)}
		}
		return []Event{done("ok")}
	}}
	h, _ := testHub(t, ad, twoModels)
	h.pol.BreakAfter = 2 // 小于单个请求的尝试次数（retries=2 → 3 次）
	collect(h.Call(context.Background(), req("n")))
	m1 := 0
	for _, c := range ad.calls {
		if c[:2] == "m1" {
			m1++
		}
	}
	if m1 != 3 {
		t.Fatalf("单个请求应把重试用完（3 次），实际 m1 被调 %d 次", m1)
	}
	if h.isOpen("a/m1") {
		t.Fatal("单个请求的重试不应打开熔断")
	}
}

func TestFirstEventTimeoutAndCancel(t *testing.T) {
	ad := &fakeAdapter{hang: true}
	h, _ := testHub(t, ad, `
[provider.a]
protocol = "fake"
[model."a/m1"]
[alias.default]
plan = [{ model = "a/m1", retries = 1 }]
`)
	_, _, _, f, terms := collect(h.Call(context.Background(), req("n")))
	if f == nil || f.Code != CodeTimeout || terms != 1 || len(ad.calls) != 2 {
		t.Fatalf("首事件超时应重试后以 TIMEOUT 结束: %v calls=%v", f, ad.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := h.Call(ctx, req("n2"))
	time.AfterFunc(20*time.Millisecond, cancel)
	_, _, _, f, _ = collect(ch)
	if f == nil || f.Code != CodeAborted {
		t.Fatalf("取消应以 ABORTED 结束: %v", f)
	}
}

func TestShapeCheckOnTransform(t *testing.T) {
	ad := &fakeAdapter{script: func(int, Endpoint) []Event { return []Event{done("ok")} }}
	h, _ := testHub(t, ad, twoModels)
	h.AddLayer(Layer{Name: "evil", Call: func(ctx context.Context, r *Request, next func(context.Context, *Request) <-chan Event) <-chan Event {
		rr := *r
		rr.Req.Tools = append(rr.Req.Tools, session.ToolSpec{Name: "smuggled"})
		return next(ctx, &rr)
	}})
	_, _, _, f, _ := collect(h.Call(context.Background(), req("n")))
	if f == nil || f.Code != CodeShape || len(ad.calls) != 0 {
		t.Fatalf("放宽工具应按结构违规拒绝、不发出: %v", f)
	}
}

func TestSideCallAndMinWindow(t *testing.T) {
	ad := &fakeAdapter{script: func(int, Endpoint) []Event { return []Event{{TextDelta: "x"}, done("摘要")} }}
	h, _ := testHub(t, ad, twoModels)
	d, f := h.SideCall(context.Background(), "default", session.Request{})
	if f != nil || d == nil {
		t.Fatalf("side_call: %v", f)
	}
	plan, _ := h.Catalog().Resolve("default")
	if w := h.Catalog().MinWindow(plan); w != 500 {
		t.Fatalf("最小窗口 = %d", w)
	}
}

func TestCatalogLayersOverride(t *testing.T) {
	cat := NewCatalog()
	b, _ := ParseCatalog(`
[provider.deepseek]
protocol = "openai-chat"
base_url = "https://builtin"
[model."deepseek/deepseek-chat"]
context_window = 1
`)
	u, _ := ParseCatalog(`
[provider.deepseek]
protocol = "openai-chat"
base_url = "https://user"
`)
	_ = cat.Add(LayerUser, "models.toml", u)
	_ = cat.Add(LayerBuiltin, "builtin", b) // 低层后加也不覆盖高层
	_, p, err := cat.Lookup("deepseek-chat")
	if err != nil || p.BaseURL != "https://user" {
		t.Fatalf("高层应胜: %+v %v", p, err)
	}
}
