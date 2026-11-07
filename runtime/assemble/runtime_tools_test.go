package assemble

import (
	"context"
	"encoding/json"
	"testing"

	"tenon/runtime/session"
	"tenon/runtime/store/memstore"
	"tenon/runtime/toolhub"
)

// 装配必须恰好有一个授权层（见 toolhub.Validate），测试里放一个直通的。
func permLayer() LayerFunc {
	return func(*SessionCx) ([]toolhub.Layer, error) {
		return []toolhub.Layer{{
			Name: "perm", Guard: true, Authorizes: true,
			Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
				return next(ctx, cx, args)
			},
		}}, nil
	}
}

// 工具在装配期构造一次、各会话共用同一批只读对象；会话差异靠 Visible 挑，不靠重造
// （框架设计 04 4.2 / 4.9：组件是全局单例，会话状态从调用上下文拿）。
func TestToolsSharedAcrossSessions(t *testing.T) {
	asked := 0
	mk := func(name string) func() []*toolhub.Tool {
		var t0 *toolhub.Tool
		return func() []*toolhub.Tool {
			asked++
			if t0 == nil {
				t0 = &toolhub.Tool{
					Spec: session.ToolSpec{Name: name, Schema: json.RawMessage(`{"type":"object"}`)},
					Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
						return toolhub.Text("ok"), nil
					},
				}
			}
			return []*toolhub.Tool{t0}
		}
	}
	echo, delegate := mk("echo"), mk("delegate")
	b := &Builder{
		DB:     memstore.New(),
		Layers: []LayerFunc{permLayer()},
		Tools: []ToolSet{
			{Tools: echo},
			{Tools: delegate, Visible: func(sc *SessionCx) bool { return !sc.Child }},
		},
	}

	a1, _, err := b.Build("s1", "", false)
	if err != nil {
		t.Fatalf("Build s1：%v", err)
	}
	a2, _, err := b.Build("s2", "", true)
	if err != nil {
		t.Fatalf("Build s2：%v", err)
	}
	// 取用函数每会话调一次（懒加载语义），但拿到的必须是同一批只读对象。
	if asked != 3 {
		t.Fatalf("取用函数调了 %d 次，应为 每会话 2 组；子会话 1 组 = 3", asked)
	}
	e1, _ := a1.Hub.Get("echo")
	e2, _ := a2.Hub.Get("echo")
	if e1 != e2 {
		t.Fatal("两个会话的 echo 不是同一个对象")
	}
	for _, n := range []string{"echo", "delegate"} {
		if _, ok := a1.Hub.Get(n); !ok {
			t.Fatalf("父会话缺 %s", n)
		}
	}
	if _, ok := a2.Hub.Get("echo"); !ok {
		t.Fatal("子会话也要 Visible 为空的那组")
	}
	if _, ok := a2.Hub.Get("delegate"); ok {
		t.Fatal("子会话不该拿到 Visible=false 的工具")
	}
	// 会话上下文随会话走：两个会话各一份 Ctx，子会话标记也在。
	if got := a1.Ctx.SessionID(); got != "s1" {
		t.Fatalf("s1 Ctx 会话 = %q", got)
	}
	if !a2.Ctx.Child() {
		t.Fatal("s2 应是子会话")
	}
}

// 一批工具取不到（如 MCP 还没起来）时按空处理，不让装配失败。
func TestToolSetNilTools(t *testing.T) {
	b := &Builder{DB: memstore.New(), Layers: []LayerFunc{permLayer()}, Tools: []ToolSet{
		{Tools: func() []*toolhub.Tool { return nil }},
		{Tools: nil},
	}}
	if _, _, err := b.Build("s", "", false); err != nil {
		t.Fatalf("Build：%v", err)
	}
}
