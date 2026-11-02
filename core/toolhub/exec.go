package toolhub

// 调用栈与并行执行（tool-hub §4.1、D6）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ybase-kernel/core/callctx"
	"ybase-kernel/core/session"
)

// UnknownOutcome 崩溃时已开始执行、结果未知的调用回给模型的文案（DSH TOOL_OUTCOME_UNKNOWN 分级写法）。
const UnknownOutcome = "工具调用在进程中断时已开始执行，结果未知。只读或幂等的操作可以重试；可能有副作用的操作请先核对当前状态，或询问用户。"

// Result 一次调用的结果（按调用顺序产出）。
type Result struct {
	Call   session.ToolCall
	Output Output
	Err    error // 基础设施错误（存储失败等）；工具自身失败在 Output.Status
}

// Run 一批调用的执行句柄。
type Run struct {
	results []chan Result
	cancel  context.CancelFunc
	i       int
	stopped chan struct{}
	once    sync.Once
}

// Next 按调用顺序取下一个结果；全部取完返回 false。
func (r *Run) Next() (Result, bool) {
	if r.i >= len(r.results) {
		return Result{}, false
	}
	res := <-r.results[r.i]
	r.i++
	return res, true
}

// Stop 取消仍在执行的调用（Read 立即中止；Write / Dangerous 等 finish_grace）。
func (r *Run) Stop() { r.once.Do(func() { close(r.stopped); r.cancel() }) }

// Execute 执行一批调用。hints 为内核校验失败的提示（按调用 id），这些调用直接回填错误、不执行。
func (h *Hub) Execute(ctx context.Context, k *callctx.Ctx, calls []session.ToolCall, hints map[string]string) *Run {
	return h.ExecuteIn(ctx, nil, k, calls, hints)
}

// ExecuteIn 同 Execute，限定在会话工具视图 ts 内（ts 非空时可用集外的调用直接回错误）。
// k 是本次调用的上下文（框架设计 04 §4.4），一路传到工具与层里。
func (h *Hub) ExecuteIn(ctx context.Context, ts *Toolset, k *callctx.Ctx, calls []session.ToolCall, hints map[string]string) *Run {
	ctx, cancel := context.WithCancel(ctx)
	run := &Run{cancel: cancel, stopped: make(chan struct{})}
	for range calls {
		run.results = append(run.results, make(chan Result, 1))
	}
	var rw sync.RWMutex
	sem := make(chan struct{}, h.maxParallel)
	// 单一分发者按调用顺序取锁：独占调用等前面全部完成，其后的调用等它完成。
	go func() {
		for i, call := range calls {
			ch := run.results[i]
			if hint, ok := hints[call.ID]; ok {
				ch <- Result{Call: call, Output: Errorf("%s", hint)}
				continue
			}
			t, ok := h.tools[call.Name]
			if ok && ts != nil && !ts.isAvailable(call.Name) {
				t, ok = nil, false // 可用集外：按不存在处理（D1 安全语义）
			}
			par := ok && t.parallel()
			if par {
				rw.RLock()
			} else {
				rw.Lock()
			}
			sem <- struct{}{}
			go func(call session.ToolCall, t *Tool, ch chan Result, par bool) {
				defer func() {
					<-sem
					if par {
						rw.RUnlock()
					} else {
						rw.Unlock()
					}
				}()
				ch <- h.one(ctx, run.stopped, ts, k, call, t)
			}(call, t, ch, par)
		}
	}()
	return run
}

// one 一次调用的完整栈。
func (h *Hub) one(ctx context.Context, stopped <-chan struct{}, ts *Toolset, k *callctx.Ctx, call session.ToolCall, t *Tool) Result {
	sessionID := k.SessionID()
	// 记录读写不随回合取消：取消时结果往往已经产生（Write 宽限期内完成、Read 返回取消结果），
	// 用已取消的 ctx 落库会变成基础设施错误让会话退出，重启后还会被报成「结果未知」。
	sctx := context.WithoutCancel(ctx)
	start := time.Now()
	ev := SpanEvent{CallID: call.ID, Tool: call.Name, Start: start}
	res := Result{Call: call}
	defer func() {
		ev.End = time.Now()
		if ev.Status == "" {
			ev.Status = statusName(res.Output.Status)
		}
		ev.ResultBytes = contentLen(res.Output.Content)
		if h.onSpan != nil {
			h.onSpan(ev)
		}
	}()
	if t == nil || (ts != nil && !ts.isAvailable(call.Name)) {
		res.Output = Errorf("工具 %s 不存在", call.Name)
		return res
	}
	ev.Risk = t.Risk.String()
	cx := &Cx{Ctx: k, Hub: h, SessionID: sessionID, CallID: call.ID, Tool: t, Toolset: ts, Args: call.Args, Items: h.ItemsOf(t)}
	defer func() { ev.ChildSessionID = cx.ChildSessionID }()

	// —— 入口：幂等 ——
	if h.store != nil {
		rec, found, err := h.store.Lookup(sctx, sessionID, call.ID)
		if err != nil {
			res.Err = err
			return res
		}
		if found {
			ev.Recovered = true
			cx.Resumed = true
			switch rec.Status {
			case "done":
				var o storedOutput
				if err := json.Unmarshal([]byte(rec.Output), &o); err != nil {
					res.Err = err
					return res
				}
				res.Output = o.output()
				return res
			case "started":
				res.Output = Output{Kind: Done, Content: session.Content{session.NewTextPart(UnknownOutcome)}, Status: session.ToolError}
				ev.Status = "unknown"
				return res
			case "delivered":
				if rec.Decision != "approve" || rec.ArgsHash != ArgsHash(call.Args) {
					out := denied(rec.Decision)
					if rec.Decision == "approve" { // 批准过，但参数哈希对不上（如旧版本写的记录）：不是用户拒绝
						out = Errorf("这次调用的批准记录与当前参数不一致（可能由旧版本写入），未执行；如仍需要，请重新发起同一调用，会再次请求确认")
					}
					res.Output, res.Err = h.finish(sctx, sessionID, call.ID, "delivered", out)
					ev.Guard = "denied"
					return res
				}
				cx.Approved = true // 恢复：已批准且参数一致
			}
		}
	}
	// —— 入口：Schema 校验（最小：必须是 JSON 对象，required 字段存在）——
	if msg := checkArgs(t.Spec.Schema, call.Args); msg != "" {
		res.Output = Errorf("工具 %s 参数不合法：%s", call.Name, msg)
		return res
	}

	out, err := h.chain(0, call)(ctx, cx, call.Args)
	if err != nil {
		res.Err = err
		return res
	}
	if out.Kind == Confirm {
		ev.Guard = "confirm"
		out, err = h.confirm(ctx, sctx, cx, call, out.Reason)
		if err != nil {
			res.Err = err
			return res
		}
	}
	if out.Kind == Done && !cx.finished {
		out, ev.Artifact = h.spill(sctx, sessionID, t, out)
		if h.store != nil {
			b, _ := json.Marshal(newStored(out))
			// 只有执行器真的写过 started 才按 started 推进；批准过（delivered）但没写 started
			// 的（只读风险工具、批准后层短路）从 delivered 推进；其余层短路直接插入 done。
			from := ""
			switch {
			case cx.started:
				from = "started"
			case cx.Approved:
				from = "delivered"
			}
			if err := h.store.FinishTool(sctx, sessionID, call.ID, from, string(b)); err != nil {
				res.Err = err
				return res
			}
		}
	}
	res.Output = out
	_ = stopped
	return res
}

// confirm Guard 返回 Confirm：写挂起记录 → 等人工决定（持有本调用的锁，保留顺序位，D6）。
// 批准则重走调用栈（Guard 按记录放行，参数哈希锁定）；拒绝 / 超时以错误结果结清。
// sctx 是不随回合取消的存储 ctx（见 one）。
func (h *Hub) confirm(ctx, sctx context.Context, cx *Cx, call session.ToolCall, reason string) (Output, error) {
	if h.await == nil || h.store == nil {
		return Errorf("需要人工确认，但当前没有可应答者：%s", reason), nil
	}
	deadline := time.Now().Add(h.deadline)
	if err := h.store.PutPending(sctx, cx.SessionID, cx.CallID, ArgsHash(call.Args), reason, deadline); err != nil {
		return Output{}, err
	}
	actx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ok, err := h.await(actx, cx, reason, deadline)
	switch {
	case err != nil && ctx.Err() != nil: // 回合取消：记录保持 pending，结果按取消回内核
		cx.finished = true
		return Output{Kind: Done, Content: session.Content{session.NewTextPart("回合已取消，确认未完成")}, Status: session.ToolTurnCancelled}, nil
	case err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(actx.Err(), context.DeadlineExceeded)): // 截止
		cx.finished = true
		return h.finish(sctx, cx.SessionID, cx.CallID, "pending", Errorf("用户未在截止时间前回应确认：%s", reason))
	case err != nil: // 应答者失败（含进程被杀）：记录保持 pending，恢复后重问
		return Output{}, err
	case !ok:
		cx.finished = true
		return h.finish(sctx, cx.SessionID, cx.CallID, "delivered", denied("deny"))
	}
	cx.Approved = true
	out, err := h.chain(0, call)(ctx, cx, call.Args)
	if err != nil {
		return Output{}, err
	}
	if out.Kind == Confirm { // 批准后 Guard 仍要求确认：按拒绝结清，不循环
		cx.finished = true
		return h.finish(sctx, cx.SessionID, cx.CallID, "delivered", Errorf("批准后仍未通过权限检查：%s", out.Reason))
	}
	return out, nil
}

// finish 以给定输出结清记录（from 为当前状态）。
func (h *Hub) finish(ctx context.Context, sid, cid, from string, out Output) (Output, error) {
	b, _ := json.Marshal(newStored(out))
	if err := h.store.FinishTool(ctx, sid, cid, from, string(b)); err != nil {
		return Output{}, err
	}
	return out, nil
}

func denied(decision string) Output {
	return Errorf("用户拒绝了这次调用（%s）", decision)
}

// chain 构造第 i 层起的调用链；最内层是执行器。
func (h *Hub) chain(i int, call session.ToolCall) Next {
	if i >= len(h.layers) {
		return h.executor
	}
	l := h.layers[i]
	inner := h.chain(i+1, call)
	return func(ctx context.Context, cx *Cx, args json.RawMessage) (Output, error) {
		// 内层（Guard）返回的 Confirm 经过 Transform 层原样上传是合法的；
		// 只有 Transform 自己造出 Confirm 才拒绝。
		innerConfirm := false
		watched := func(ctx context.Context, cx *Cx, args json.RawMessage) (Output, error) {
			o, e := inner(ctx, cx, args)
			if e == nil && o.Kind == Confirm {
				innerConfirm = true
			}
			return o, e
		}
		out, err := runLayer(ctx, l, cx, args, watched)
		if err != nil {
			if l.Guard || !l.FailOpen {
				return Errorf("调用被 %s 拒绝：%v", l.Name, err), nil // Guard FailClosed
			}
			return inner(ctx, cx, args) // Transform FailOpen：跳过本层
		}
		if out.Kind == Confirm && !l.Guard && !innerConfirm {
			return Errorf("层 %s 不允许返回 Confirm（只能由 Guard 返回）", l.Name), nil
		}
		return out, nil
	}
}

var errLayerTimeout = errors.New("层超时")

// runLayer 执行一层；超时只计本层自身耗时（扣除 next 的耗时）。
func runLayer(ctx context.Context, l Layer, cx *Cx, args json.RawMessage, next Next) (out Output, err error) {
	var innerDur time.Duration
	var mu sync.Mutex
	wrapped := func(ctx context.Context, cx *Cx, args json.RawMessage) (Output, error) {
		s := time.Now()
		o, e := next(ctx, cx, args)
		mu.Lock()
		innerDur += time.Since(s)
		mu.Unlock()
		return o, e
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	start := time.Now()
	out, err = l.Call(ctx, cx, args, wrapped)
	mu.Lock()
	own := time.Since(start) - innerDur
	mu.Unlock()
	if err == nil && l.Timeout > 0 && own > l.Timeout {
		return Output{}, errLayerTimeout
	}
	return out, err
}

// executor 最内层：有副作用的写 started，按超时执行。
func (h *Hub) executor(ctx context.Context, cx *Cx, args json.RawMessage) (Output, error) {
	t := cx.Tool
	if h.store != nil && t.Risk != Read {
		if ctx.Err() != nil { // 回合已取消：不再开始有副作用的执行
			return Output{Kind: Done, Content: session.Content{session.NewTextPart("回合已取消，未执行")}, Status: session.ToolTurnCancelled}, nil
		}
		if err := h.store.PutToolStarted(context.WithoutCancel(ctx), cx.SessionID, cx.CallID, ArgsHash(args)); err != nil {
			return Output{}, err
		}
		cx.started = true
	}
	// Read 随回合取消立即中止；Write / Dangerous 不随取消中止，最多再等 finish_grace。
	runCtx := ctx
	var cancel context.CancelFunc
	if t.Risk == Read {
		runCtx, cancel = context.WithTimeout(ctx, t.timeout(h.timeout))
	} else {
		runCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), t.timeout(h.timeout))
		stop := context.AfterFunc(ctx, func() {
			timer := time.AfterFunc(DefaultFinishGrace, cancel)
			_ = timer
			if t.Cancel != nil {
				t.Cancel(context.Background(), cx)
			}
		})
		defer stop()
	}
	defer cancel()
	out, err := safeCall(runCtx, t, cx, args)
	if err != nil {
		return Errorf("%v", err), nil
	}
	if ctx.Err() != nil && t.Risk == Read && out.Kind == Done && runCtx.Err() != nil {
		out.Status = session.ToolTurnCancelled
	}
	return out, nil
}

func safeCall(ctx context.Context, t *Tool, cx *Cx, args json.RawMessage) (out Output, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("工具 panic: %v", r)
		}
	}()
	return t.Call(ctx, cx, args)
}

// ArgsHash 参数哈希（确认时锁定参数，tool-hub D4）。
func ArgsHash(args json.RawMessage) string {
	// 规范化：日志序列化会压缩 RawMessage，内存中的原始参数与恢复后的参数字节不同，哈希必须按压缩形式算。
	// json.Marshal(RawMessage) 与日志序列化同形：压缩并把 < > & 转义为 \u003c 等；只做 Compact 不转义，
	// 含 2>&1 的命令恢复后哈希会变，批准记录推进失败（“工具记录状态不符”）。
	if b, err := json.Marshal(json.RawMessage(args)); err == nil {
		args = b
	}
	sum := sha256.Sum256(args)
	return hex.EncodeToString(sum[:])
}

func checkArgs(schema, args json.RawMessage) string {
	var obj map[string]json.RawMessage
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(args, &obj); err != nil {
		return "必须是 JSON 对象"
	}
	var s struct {
		Required []string `json:"required"`
	}
	if len(schema) > 0 && json.Unmarshal(schema, &s) == nil {
		var miss []string
		for _, r := range s.Required {
			if _, ok := obj[r]; !ok {
				miss = append(miss, r)
			}
		}
		if len(miss) > 0 {
			return "缺少必填字段 " + strings.Join(miss, ", ")
		}
	}
	return ""
}

type storedOutput struct {
	Text   string `json:"text"`
	Error  bool   `json:"error,omitempty"`
	Status string `json:"status,omitempty"`
}

func newStored(o Output) storedOutput {
	return storedOutput{Text: contentText(o.Content), Error: o.Status == session.ToolError, Status: statusName(o.Status)}
}

func (s storedOutput) output() Output {
	st := session.ToolOK
	if s.Error {
		st = session.ToolError
	}
	return Output{Kind: Done, Content: session.Content{session.NewTextPart(s.Text)}, Status: st}
}

func statusName(s session.ToolStatus) string {
	if s.State == "" {
		return "ok"
	}
	if s.CancelReason != "" {
		return s.State + ":" + s.CancelReason
	}
	return s.State
}

func contentText(c session.Content) string {
	var b strings.Builder
	for _, p := range c {
		if p.Kind == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func contentLen(c session.Content) int { return len(contentText(c)) }
