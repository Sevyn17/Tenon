package modelhub

// 调度器（model-hub §4.2 / §4.6，D1 / D6，review-1 R2 / R3 / G5）。
//
// 别名 → 降级计划；逐项：熔断打开则跳过 → 单次调用适配器 → 成功返回；
// 可重试失败在本项内退避重试（遵守边界内的 Retry-After），已发出增量时先发 Reset；
// Auth / InvalidRequest / NoAdapter / MissingCredential 换下一项；ContextOverflow 原样上抛。
// 三类超时：首事件、流式空闲、整个计划的 deadline。每次尝试回调一个 AttemptSpan。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"ybase/runtime/session"
)

// Policy 调度默认值（§4.6）。
type Policy struct {
	Retries       int
	Initial       time.Duration
	MaxDelay      time.Duration
	MaxRetryAfter time.Duration
	FirstEvent    time.Duration
	Idle          time.Duration
	Deadline      time.Duration
	BreakAfter    int
	BreakFor      time.Duration
}

// DefaultPolicy 默认值。
var DefaultPolicy = Policy{Retries: 5, Initial: 500 * time.Millisecond, MaxDelay: 10 * time.Second,
	MaxRetryAfter: 60 * time.Second, FirstEvent: 60 * time.Second, Idle: 120 * time.Second,
	Deadline: 10 * time.Minute, BreakAfter: 5, BreakFor: 30 * time.Second}

// AttemptSpan 一次尝试的观测（review-1 G2 / G5，M3.14）。
type AttemptSpan struct {
	RequestID, AttemptKey, Model, Provider, ShellHash string
	Step, Attempt                                     int
	Start, End                                        time.Time
	TTFT, Gen                                         time.Duration
	Code                                              Code
	Usage                                             session.Usage
}

// Layer 模型调用层（Transform / Guard），由外到内。
type Layer struct {
	Name     string
	Guard    bool
	FailOpen bool // 只对 Transform 有效
	Call     func(ctx context.Context, r *Request, next func(context.Context, *Request) <-chan Event) <-chan Event
}

// Hub 模型管理器。
type Hub struct {
	cat      *Catalog
	adapters map[string]Adapter
	secret   func(ref string) (string, error)
	layers   []Layer
	pol      Policy
	OnSpan   func(AttemptSpan)

	mu      sync.Mutex
	breaker map[string]*breaker // provider/model → 熔断状态
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
}

type breaker struct {
	fails     int
	openUntil time.Time
}

// New 构造。secret 解析凭证引用（Secrets 插槽）。
func New(cat *Catalog, secret func(string) (string, error), pol *Policy) *Hub {
	h := &Hub{cat: cat, adapters: map[string]Adapter{}, secret: secret, pol: DefaultPolicy,
		breaker: map[string]*breaker{}, now: time.Now, sleep: sleepCtx}
	if pol != nil {
		h.pol = *pol
	}
	return h
}

// RegisterAdapter 登记协议适配器（甲类点位）。
func (h *Hub) RegisterAdapter(a Adapter) error {
	if _, dup := h.adapters[a.Protocol()]; dup {
		return fmt.Errorf("modelhub: 协议 %s 重复登记", a.Protocol())
	}
	h.adapters[a.Protocol()] = a
	return nil
}

// AddLayer 追加模型层（调用方已按 plugin-manager §5.2 排序）。
func (h *Hub) AddLayer(l Layer) { h.layers = append(h.layers, l) }

// Catalog 目录。
func (h *Hub) Catalog() *Catalog { return h.cat }

// Call 会话调用：Transform → Guard → 结构校验（R2）→ 调度器 → 适配器。
// alias 取 r.Req.Model。返回的通道恰好有一个终止事件。
func (h *Hub) Call(ctx context.Context, r *Request) <-chan Event {
	base := r.Req
	return h.chain(0, base)(ctx, r)
}

// SideCall 侧调用：不经层，直进调度器，不发增量（只返回终止事件）。取消随 ctx（§4.4）。
func (h *Hub) SideCall(ctx context.Context, alias string, req session.Request) (*Done, *Failure) {
	req.Model = alias
	r := &Request{Req: req, RequestID: "side-" + newID()}
	var done *Done
	var fail *Failure
	for ev := range h.schedule(ctx, r) {
		if ev.Done != nil {
			done = ev.Done
		}
		if ev.Failed != nil {
			fail = ev.Failed
		}
	}
	return done, fail
}

func (h *Hub) chain(i int, base session.Request) func(context.Context, *Request) <-chan Event {
	if i >= len(h.layers) {
		return func(ctx context.Context, r *Request) <-chan Event {
			// 没有模型层时请求原样到这里，前缀必然相同：不做逐条 JSON 比对（审查 P1-39：原先每次调模型都把
			// 整段历史序列化两遍，生产上没有模型层，纯空转）。
			if len(h.layers) == 0 {
				return h.schedule(ctx, r)
			}
			if err := checkShape(base, r.Req); err != nil {
				return failNow(&Failure{Code: CodeShape, Message: err.Error()})
			}
			return h.schedule(ctx, r)
		}
	}
	l := h.layers[i]
	inner := h.chain(i+1, base)
	return func(ctx context.Context, r *Request) (out <-chan Event) {
		defer func() {
			if p := recover(); p != nil {
				if l.Guard || !l.FailOpen {
					out = failNow(&Failure{Code: CodeShape, Message: fmt.Sprintf("层 %s 失败：%v", l.Name, p)})
					return
				}
				out = inner(ctx, r)
			}
		}()
		return l.Call(ctx, r, inner)
	}
}

// checkShape 结构校验入口（测试可替换以计数）。
var checkShape = CheckShape

// CheckShape R2：最终请求的消息以基础请求为前缀（逐字节），工具是子集且定义不变。
func CheckShape(base, final session.Request) error {
	if len(final.Messages) < len(base.Messages) {
		return fmt.Errorf("消息被删减（%d < %d）", len(final.Messages), len(base.Messages))
	}
	for i := range base.Messages {
		if !sameJSON(base.Messages[i], final.Messages[i]) {
			return fmt.Errorf("前缀第 %d 条消息被改写", i)
		}
	}
	allowed := map[string]string{}
	for _, t := range base.Tools {
		b, _ := json.Marshal(t)
		allowed[t.Name] = string(b)
	}
	for _, t := range final.Tools {
		b, _ := json.Marshal(t)
		if w, ok := allowed[t.Name]; !ok || w != string(b) {
			return fmt.Errorf("工具 %q 被放宽或改写", t.Name)
		}
	}
	return nil
}

// schedule 逐计划项执行。
func (h *Hub) schedule(ctx context.Context, r *Request) <-chan Event {
	out := make(chan Event, 64)
	go func() {
		defer close(out)
		plan, err := h.cat.Resolve(r.Req.Model)
		if err != nil {
			out <- Event{Failed: &Failure{Code: catalogCode(err), Message: err.Error()}}
			return
		}
		dctx, cancel := context.WithTimeout(ctx, h.pol.Deadline)
		defer cancel()
		var last *Failure
		emitted := false
		for si, step := range plan {
			f := h.runStep(dctx, r, si, step, out, &emitted)
			if f == nil {
				return
			}
			last = f
			if f.Code == CodeContextOverflow || f.Code == CodeAborted || dctx.Err() != nil {
				break
			}
		}
		if ctx.Err() != nil && last != nil {
			last.Code = CodeAborted
		} else if dctx.Err() != nil && last != nil && last.Code != CodeAborted {
			last.Code = CodeTimeout
		}
		out <- Event{Failed: last}
	}()
	return out
}

// runStep 一个计划项（含本项内重试）；成功时已把 Done 送出并返回 nil。
func (h *Hub) runStep(ctx context.Context, r *Request, si int, step PlanStep, out chan<- Event, emitted *bool) *Failure {
	m, p, err := h.cat.Lookup(step.Model)
	if err != nil {
		return &Failure{Code: catalogCode(err), Message: err.Error()}
	}
	key := m.Provider + "/" + step.Model
	if h.isOpen(key) {
		return &Failure{Code: CodeCircuitOpen, Message: key + " 熔断中"}
	}
	ad, ok := h.adapters[p.Protocol]
	if !ok {
		return &Failure{Code: CodeNoAdapter, Message: "无协议适配器 " + p.Protocol}
	}
	apiKey, err := h.secret(p.Key)
	if err != nil || (apiKey == "" && p.Key != "") {
		return &Failure{Code: CodeMissingCred, Message: fmt.Sprintf("提供方 %s 凭证 %s 不可用", m.Provider, p.Key)}
	}
	name := m.Name
	if name == "" {
		name = step.Model
	}
	ep := Endpoint{Provider: m.Provider, BaseURL: p.BaseURL, APIKey: apiKey, Model: name, Compat: p.Compat}
	retries := step.Retries
	if retries <= 0 {
		retries = p.Retry.Max
	}
	if retries <= 0 {
		retries = h.pol.Retries
	}
	attemptKey := fmt.Sprintf("%s#%d", r.RequestID, si)
	shell := shellHash(r.Req)
	delay := h.pol.Initial
	var last *Failure
	// 熔断按「请求」计数，不按「尝试」（C9）：本项重试的中间失败不记；本项最终成功记一次成功、
	// 最终以可重试错误失败记一次失败。否则单个请求自己的 5 次重试就能把熔断打开，殃及所有会话，
	// 而且第 5 次重试会因为熔断已开被跳过。
	for at := 0; at <= retries; at++ {
		rr := *r
		rr.AttemptKey = attemptKey
		f := h.attempt(ctx, &rr, ad, ep, out, emitted, AttemptSpan{RequestID: r.RequestID, AttemptKey: attemptKey,
			Model: step.Model, Provider: m.Provider, ShellHash: shell, Step: si, Attempt: at})
		if f == nil {
			h.record(key, true)
			return nil
		}
		last = f
		if !f.Code.Retryable() || ctx.Err() != nil {
			if f.Code.Retryable() && ctx.Err() == nil {
				h.record(key, false)
			}
			return f
		}
		if at == retries {
			h.record(key, false)
			break
		}
		wait := backoff(delay)
		if f.RetryAfter > 0 {
			if f.RetryAfter > h.pol.MaxRetryAfter {
				h.record(key, false) // 本项到此为止：按一次失败记
				return f             // 超上限：换下一项
			}
			wait = f.RetryAfter
		}
		if err := h.sleep(ctx, wait); err != nil {
			return &Failure{Code: CodeAborted, Message: "退避时取消", Partial: f.Partial}
		}
		delay *= 2
		if delay > h.pol.MaxDelay {
			delay = h.pol.MaxDelay
		}
	}
	return last
}

// attempt 单次尝试：转发增量、套首事件 / 空闲超时、回调 span。
func (h *Hub) attempt(ctx context.Context, r *Request, ad Adapter, ep Endpoint, out chan<- Event, emitted *bool, sp AttemptSpan) (fail *Failure) {
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	sp.Start = h.now()
	defer func() {
		sp.End = h.now()
		if fail != nil {
			sp.Code = fail.Code
		}
		if sp.TTFT > 0 {
			sp.Gen = sp.End.Sub(sp.Start) - sp.TTFT
		}
		if h.OnSpan != nil {
			h.OnSpan(sp)
		}
	}()
	if *emitted {
		out <- Event{Reset: true} // StreamReset：上一尝试的增量作废
		*emitted = false
	}
	ch := ad.Stream(actx, r, ep)
	timer := time.NewTimer(h.pol.FirstEvent)
	defer timer.Stop()
	first := true
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return &Failure{Code: CodeProtocol, Message: "适配器未发终止事件"}
			}
			if first {
				first = false
				sp.TTFT = h.now().Sub(sp.Start)
			}
			timer.Reset(h.pol.Idle)
			switch {
			case ev.Done != nil:
				sp.Usage = ev.Done.Reply.Usage
				out <- ev
				return nil
			case ev.Failed != nil:
				return ev.Failed
			default:
				if ev.TextDelta != "" || ev.ReasoningDelta != "" {
					*emitted = true
				}
				out <- ev
			}
		case <-timer.C:
			cancel()
			msg := "流式空闲超时"
			if first {
				msg = "首事件超时"
			}
			drainUntilClosed(ch)
			return &Failure{Code: CodeTimeout, Message: msg}
		case <-ctx.Done():
			cancel()
			partial := drainPartial(ch)
			return &Failure{Code: CodeAborted, Message: "已取消", Partial: partial}
		}
	}
}

func (h *Hub) isOpen(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.breaker[key]
	return b != nil && h.now().Before(b.openUntil)
}

func (h *Hub) record(key string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.breaker[key]
	if b == nil {
		b = &breaker{}
		h.breaker[key] = b
	}
	if ok {
		b.fails = 0
		return
	}
	b.fails++
	if b.fails >= h.pol.BreakAfter {
		b.openUntil = h.now().Add(h.pol.BreakFor)
		b.fails = 0
	}
}

// —— 工具函数 ——//

func backoff(d time.Duration) time.Duration {
	j := 1 + (rand.Float64()*0.2 - 0.1) // ±10% 抖动
	return time.Duration(float64(d) * j)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func failNow(f *Failure) <-chan Event {
	ch := make(chan Event, 1)
	ch <- Event{Failed: f}
	close(ch)
	return ch
}

func drainUntilClosed(ch <-chan Event) {
	go func() {
		for range ch {
		}
	}()
}

// drainPartial 取消后读完适配器的终止事件，拿到已收到的部分回复。
func drainPartial(ch <-chan Event) *session.AssistantItem {
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if ev.Failed != nil {
				return ev.Failed.Partial
			}
			if ev.Done != nil {
				r := ev.Done.Reply
				return &r
			}
		case <-t.C:
			drainUntilClosed(ch)
			return nil
		}
	}
}

func shellHash(r session.Request) string {
	b, _ := json.Marshal(struct {
		M string
		S []session.SystemBlock
		T []session.ToolSpec
		P session.Params
	}{r.Model, r.System, r.Tools, r.Params})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8])
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// catalogCode 目录查找失败的失败码：没有这个模型 / 别名报 UNKNOWN_MODEL，其余（提供方未声明等）报 NO_ADAPTER。
func catalogCode(err error) Code {
	var u ErrUnknownAlias
	if errors.As(err, &u) {
		return CodeUnknownModel
	}
	return CodeNoAdapter
}
