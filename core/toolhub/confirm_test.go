package toolhub

// 人工确认单一路径（session-host D3、tool-hub D4）：Guard 返回 Confirm → 写挂起记录 → 等决定；
// 批准后重走调用栈（Guard 按记录放行、参数哈希锁定），拒绝 / 截止以错误结果结清。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ybase-kernel/core/session"
)

// approvalGuard：有 delivered+approve 且参数哈希一致的记录才放行，否则要求确认。
func approvalGuard(st *memStore) Layer {
	return Layer{Name: "perm", Guard: true, Call: func(ctx context.Context, cx *Cx, args json.RawMessage, next Next) (Output, error) {
		if r, ok, _ := st.Lookup(ctx, cx.SessionID, cx.CallID); ok && r.Status == "delivered" &&
			r.Decision == "approve" && r.ArgsHash == ArgsHash(args) {
			return next(ctx, cx, args)
		}
		return Output{Kind: Confirm, Reason: "写操作"}, nil
	}}
}

func confirmHub(t *testing.T, st *memStore, await Await) (*Hub, *int) {
	t.Helper()
	ran := 0
	h := New(Options{Store: st, Await: await})
	_ = h.Register(&Tool{Spec: spec("w"), Risk: Write,
		Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { ran++; return Text("written"), nil }})
	h.AddLayer(approvalGuard(st))
	return h, &ran
}

func TestConfirmApprove(t *testing.T) {
	st := newMem()
	asked := 0
	h, ran := confirmHub(t, st, func(_ context.Context, cx *Cx, _ string, _ time.Time) (bool, error) {
		asked++
		st.decide(cx.CallID, "approve")
		return true, nil
	})
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "w", `{"a":1}`)}, nil))
	if asked != 1 || *ran != 1 || text(rs[0].Output) != "written" {
		t.Fatalf("批准后应执行一次: asked=%d ran=%d out=%q", asked, *ran, text(rs[0].Output))
	}
	if r, _, _ := st.Lookup(context.Background(), "s", "1"); r.Status != "done" {
		t.Fatalf("记录应结清为 done: %+v", r)
	}
}

func TestConfirmDenyAndDeadline(t *testing.T) {
	st := newMem()
	h, ran := confirmHub(t, st, func(_ context.Context, cx *Cx, _ string, _ time.Time) (bool, error) {
		st.decide(cx.CallID, "deny")
		return false, nil
	})
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "w", `{}`)}, nil))
	if *ran != 0 || rs[0].Output.Status != session.ToolError {
		t.Fatalf("拒绝不应执行: ran=%d %+v", *ran, rs[0].Output)
	}

	st2 := newMem()
	h2, ran2 := confirmHub(t, st2, func(ctx context.Context, _ *Cx, _ string, _ time.Time) (bool, error) {
		return false, context.DeadlineExceeded
	})
	rs = drain(h2.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "w", `{}`)}, nil))
	if *ran2 != 0 || !strings.Contains(text(rs[0].Output), "未在截止时间前回应") {
		t.Fatalf("截止应结清为错误: ran=%d %q", *ran2, text(rs[0].Output))
	}
}

// 批准锁定参数哈希：批准的是 {"a":1}，同一调用 id 以 {"a":2} 重发不得放行。
func TestConfirmArgsHashLocked(t *testing.T) {
	st := newMem()
	st.recs["1"] = &Record{Status: "delivered", Decision: "approve", ArgsHash: ArgsHash(json.RawMessage(`{"a":1}`))}
	h, ran := confirmHub(t, st, func(context.Context, *Cx, string, time.Time) (bool, error) {
		return false, context.DeadlineExceeded
	})
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "w", `{"a":2}`)}, nil))
	if *ran != 0 || rs[0].Output.Status != session.ToolError {
		t.Fatalf("参数变化后不应凭旧批准执行: ran=%d %q", *ran, text(rs[0].Output))
	}
}

// 回合取消：等待中断，记录保持 pending（恢复后仍在等），结果按取消交内核。
func TestConfirmCancelKeepsPending(t *testing.T) {
	st := newMem()
	ctx, cancel := context.WithCancel(context.Background())
	h, ran := confirmHub(t, st, func(actx context.Context, _ *Cx, _ string, _ time.Time) (bool, error) {
		cancel()
		<-actx.Done()
		return false, actx.Err()
	})
	rs := drain(h.Execute(ctx, kctx("s"), []session.ToolCall{call("1", "w", `{}`)}, nil))
	if *ran != 0 || rs[0].Output.Status != session.ToolTurnCancelled {
		t.Fatalf("取消: ran=%d %+v", *ran, rs[0].Output)
	}
	if r, _, _ := st.Lookup(context.Background(), "s", "1"); r.Status != "pending" {
		t.Fatalf("取消后记录应保持 pending: %+v", r)
	}
}

// 层短路（Guard 直接返回结果、执行器没跑）时，有副作用的工具也要能结清记录。
func TestGuardShortCircuitWriteTool(t *testing.T) {
	st := newMem()
	h := New(Options{Store: st})
	h.Register(&Tool{Spec: spec("w"), Risk: Write,
		Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { return Text("ran"), nil }})
	h.AddLayer(Layer{Name: "deny", Guard: true, Call: func(ctx context.Context, cx *Cx, a json.RawMessage, next Next) (Output, error) {
		return Errorf("不行"), nil
	}})
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("c1", "w", `{}`)}, nil))
	if len(rs) != 1 || rs[0].Err != nil || rs[0].Output.Status != session.ToolError {
		t.Fatalf("短路结果应为错误结果而非内部错误: %+v", rs)
	}
	if r, _, _ := st.Lookup(context.Background(), "s", "c1"); r.Status != "done" {
		t.Fatalf("短路后记录应为 done: %+v", r)
	}
}

// 只读风险的工具也可能要确认（如联网的 web_fetch）：批准后记录是 delivered，执行器不写 started，
// 结清必须从 delivered 推进，不能当作新记录插入。
func TestConfirmApproveReadRiskTool(t *testing.T) {
	st := newMem()
	ran := 0
	h := New(Options{Store: st, Await: func(_ context.Context, cx *Cx, _ string, _ time.Time) (bool, error) {
		st.decide(cx.CallID, "approve")
		return true, nil
	}})
	_ = h.Register(&Tool{Spec: spec("fetch"), Risk: Read,
		Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { ran++; return Text("page"), nil }})
	h.AddLayer(approvalGuard(st))
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "fetch", `{}`)}, nil))
	if rs[0].Err != nil || ran != 1 || text(rs[0].Output) != "page" {
		t.Fatalf("批准后只读工具应执行并结清: ran=%d err=%v out=%q", ran, rs[0].Err, text(rs[0].Output))
	}
	if r, _, _ := st.Lookup(context.Background(), "s", "1"); r.Status != "done" {
		t.Fatalf("记录应结清为 done: %+v", r)
	}
}

// 回合取消时结果记录仍要写成功：存储调用不能用已取消的回合 ctx。
func TestCancelledTurnStillRecordsResult(t *testing.T) {
	st := &ctxStore{memStore: newMem()}
	h := New(Options{Store: st})
	ctx, cancel := context.WithCancel(context.Background())
	_ = h.Register(&Tool{Spec: spec("w"), Risk: Write,
		Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { cancel(); return Text("written"), nil }})
	rs := drain(h.Execute(ctx, kctx("s"), []session.ToolCall{call("1", "w", `{}`)}, nil))
	if rs[0].Err != nil {
		t.Fatalf("取消不应变成基础设施错误: %v", rs[0].Err)
	}
	if r, _, _ := st.Lookup(context.Background(), "s", "1"); r.Status != "done" {
		t.Fatalf("结果已知，记录应为 done（否则重启后报结果未知）: %+v", r)
	}
}

// ctxStore 像真实存储一样：ctx 已取消的写入失败。
type ctxStore struct{ *memStore }

func (s *ctxStore) PutToolStarted(ctx context.Context, sid, id, h string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.memStore.PutToolStarted(ctx, sid, id, h)
}

func (s *ctxStore) FinishTool(ctx context.Context, sid, id, from, out string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.memStore.FinishTool(ctx, sid, id, from, out)
}

// 外层 Transform（如 redact）包着内层 Guard：Guard 的 Confirm 要能穿过 Transform 进入确认流程。
func TestConfirmThroughTransform(t *testing.T) {
	st := newMem()
	asked := 0
	h, ran := confirmHub(t, st, func(_ context.Context, cx *Cx, _ string, _ time.Time) (bool, error) {
		asked++
		st.decide(cx.CallID, "approve")
		return true, nil
	})
	h.layers = append([]Layer{{Name: "redact", Call: func(ctx context.Context, cx *Cx, a json.RawMessage, next Next) (Output, error) {
		return next(ctx, cx, a)
	}}}, h.layers...)
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "w", `{}`)}, nil))
	if asked != 1 || *ran != 1 || text(rs[0].Output) != "written" {
		t.Fatalf("asked=%d ran=%d %+v", asked, *ran, rs[0].Output)
	}
}

// Transform 自己返回 Confirm 仍被拒。
func TestTransformOwnConfirmRejected(t *testing.T) {
	h := New(Options{})
	_ = h.Register(&Tool{Spec: spec("r"), Risk: Read,
		Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { return Text("x"), nil }})
	h.AddLayer(Layer{Name: "bad", Call: func(ctx context.Context, cx *Cx, a json.RawMessage, next Next) (Output, error) {
		return Output{Kind: Confirm, Reason: "?"}, nil
	}})
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "r", `{}`)}, nil))
	if rs[0].Output.Status != session.ToolError {
		t.Fatalf("%+v", rs[0].Output)
	}
}
