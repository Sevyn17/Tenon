package assemble

// 锁定 ToolCall 中间件在真实会话 Hub 里的嵌套顺序，以及 ToolCallReq.Attrs 的传递（docs/50 M9 T1）。
//
// ToolCallTransforms 按登记顺序生成 [先, 后]，Build 逐个 PrependTransform，
// 所以实际是**后登记的在外层**（与 component.Wrap 注释「先登记的在外层」相反）。
// 文件检查点依赖这个顺序判断自己是否包在钩子外面；顺序若被改动，这个测试会先失败。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func TestToolCallMiddlewareOrderAndAttrs(t *testing.T) {
	var trace []string
	var attrs []map[string]any
	mw := func(name string) component.ToolCallMw {
		return func(ctx context.Context, req component.ToolCallReq,
			next func(context.Context, component.ToolCallReq) (component.ToolCallResp, error)) (component.ToolCallResp, error) {
			trace = append(trace, name+">")
			attrs = append(attrs, req.Attrs)
			r, err := next(ctx, req)
			trace = append(trace, "<"+name)
			return r, err
		}
	}
	asm := ctxTestAssembly(t, func(cx *component.Registrar) error {
		component.Wrap(cx, component.ToolCall, mw("first"))
		component.Wrap(cx, component.ToolCall, mw("second"))
		return nil
	})
	if got := asm.MiddlewareNames(component.PointToolCallMw); len(got) != 2 {
		t.Fatalf("应有两条中间件：%v", got)
	}
	tool := &toolhub.Tool{Spec: session.ToolSpec{Name: "write_file", Schema: json.RawMessage(`{"type":"object"}`)},
		Risk:  toolhub.Write,
		Attrs: map[string]any{toolhub.AttrWritePath: "path"},
		Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
			trace = append(trace, "tool")
			return toolhub.Text("ok"), nil
		}}
	b := &Builder{DB: memstore.New(), Inits: []InitFunc{permInit()}, ToolCallMW: asm.ToolCallTransforms(),
		Tools: []ToolSet{{Tools: func() []*toolhub.Tool { return []*toolhub.Tool{tool} }}}}
	a, _, err := b.Build("s1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	run := a.Hub.ExecuteIn(context.Background(), a.Toolset, callctx.New(callctx.Options{SessionID: "s1"}),
		[]session.ToolCall{{ID: "c1", Name: "write_file", Args: json.RawMessage(`{"path":"a.txt"}`)}}, nil)
	if res, _ := run.Next(); res.Output.Status != session.ToolOK {
		t.Fatalf("调用应成功：%+v", res.Output)
	}
	want := []string{"second>", "first>", "tool", "<first", "<second"}
	if len(trace) != len(want) {
		t.Fatalf("嵌套顺序 %v，期望 %v", trace, want)
	}
	for i := range want {
		if trace[i] != want[i] {
			t.Fatalf("嵌套顺序 %v，期望 %v（后登记的在外层）", trace, want)
		}
	}
	for i, m := range attrs {
		if m[toolhub.AttrWritePath] != "path" {
			t.Fatalf("第 %d 层中间件没拿到工具 Attrs：%v", i, m)
		}
	}
}
