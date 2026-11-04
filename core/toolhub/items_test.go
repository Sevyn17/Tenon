package toolhub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ybase-kernel/core/session"
)

func itemTool(name string, ids ...string) *Tool {
	t := &Tool{Spec: session.ToolSpec{Name: name, Schema: json.RawMessage(`{"type":"object"}`)}, Risk: Write,
		Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { return Text("ran"), nil }}
	return t.WithItems(ids...)
}

func authLayer() Layer {
	return Layer{Name: "perm", Guard: true, Authorizes: true,
		Call: func(ctx context.Context, cx *Cx, args json.RawMessage, next Next) (Output, error) {
			return next(ctx, cx, args)
		}}
}

// 权限项结构校验：类别必须在九类别内，三种类型各自的必填项。
func TestItemValidate(t *testing.T) {
	sub := func(string, json.RawMessage) ([]string, error) { return nil, nil }
	cases := []struct {
		name string
		it   Item
		bad  string
	}{
		{"switch 合法", Item{ID: "a.b", Title: "甲", Type: ItemSwitch, Class: EffectWrite}, ""},
		{"缺 ID", Item{Title: "甲", Type: ItemSwitch, Class: EffectWrite}, "缺 ID"},
		{"缺 Title", Item{ID: "a.b", Type: ItemSwitch, Class: EffectWrite}, "缺 Title"},
		{"类别不在九类内", Item{ID: "a.b", Title: "甲", Type: ItemSwitch, Class: "danger"}, "不在九个类别内"},
		{"switch 带 Subject", Item{ID: "a.b", Title: "甲", Type: ItemSwitch, Class: EffectWrite, Subject: sub}, "不应带 Subject"},
		{"match 缺 Subject", Item{ID: "a.b", Title: "甲", Type: ItemMatch, Class: EffectExec}, "必须提供 Subject"},
		{"match 合法", Item{ID: "a.b", Title: "甲", Type: ItemMatch, Class: EffectExec, Subject: sub}, ""},
		{"level 缺 Levels", Item{ID: "a.b", Title: "甲", Type: ItemLevel, Class: EffectExec}, "必须提供 Levels"},
		{"level 合法", Item{ID: "a.b", Title: "甲", Type: ItemLevel, Class: EffectExec, Levels: []string{"off", "on"}}, ""},
		{"类型非法", Item{ID: "a.b", Title: "甲", Type: "weird", Class: EffectExec}, "非法"},
		{"建议值非法", Item{ID: "a.b", Title: "甲", Type: ItemSwitch, Class: EffectWrite,
			Suggest: map[string]ItemAction{"workspace": "yolo"}}, "建议值"},
	}
	for _, c := range cases {
		err := c.it.Validate()
		if c.bad == "" {
			if err != nil {
				t.Errorf("%s：应通过，得到 %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.bad) {
			t.Errorf("%s：应报含 %q 的错，得到 %v", c.name, c.bad, err)
		}
	}
}

// 注册表：重名报错并指出来源；Items 按 id 排序；ItemsOf 只返回已注册项。
func TestRegisterItems(t *testing.T) {
	h := New(Options{})
	must := func(it Item) {
		t.Helper()
		if err := h.RegisterItem(it); err != nil {
			t.Fatal(err)
		}
	}
	must(Item{ID: "shell.exec", Title: "执行命令", Type: ItemSwitch, Class: EffectExec, Owner: "shell"})
	must(Item{ID: "fs.write", Title: "写文件", Type: ItemSwitch, Class: EffectWrite, Owner: "fs"})
	err := h.RegisterItem(Item{ID: "fs.write", Title: "重复", Type: ItemSwitch, Class: EffectWrite, Owner: "other"})
	if err == nil || !strings.Contains(err.Error(), "fs") {
		t.Fatalf("重名应报错并指出原声明者：%v", err)
	}
	ids := []string{}
	for _, it := range h.Items() {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, ",") != "fs.write,shell.exec" {
		t.Fatalf("Items 应按 id 排序：%v", ids)
	}
	if _, ok := h.LookupItem("shell.exec"); !ok {
		t.Fatal("LookupItem 取不到已注册项")
	}
	tool := itemTool("write_file", "fs.write", "nope")
	if got := h.ItemsOf(tool); len(got) != 1 || got[0].ID != "fs.write" {
		t.Fatalf("ItemsOf 应只返回已注册项：%+v", got)
	}
	if h.ItemsOf(nil) != nil {
		t.Fatal("ItemsOf(nil) 应为空")
	}
}

// 装配期校验：引用未注册项、既 NoAuth 又引用项、授权层数不等于一，都拒绝启动。
func TestHubValidate(t *testing.T) {
	newHub := func() *Hub {
		h := New(Options{})
		_ = h.RegisterItem(Item{ID: "fs.write", Title: "写文件", Type: ItemSwitch, Class: EffectWrite, Owner: "fs"})
		return h
	}

	h := newHub()
	_ = h.Register(itemTool("write_file", "fs.write"))
	if err := h.Validate(); err == nil || !strings.Contains(err.Error(), "恰好有一个权限策略层") {
		t.Fatalf("没有授权层应拒绝启动：%v", err)
	}
	h.AddLayer(authLayer())
	if err := h.Validate(); err != nil {
		t.Fatalf("一个授权层应通过：%v", err)
	}
	h.AddLayer(authLayer())
	if err := h.Validate(); err == nil || !strings.Contains(err.Error(), "现在有 2 个") {
		t.Fatalf("两个授权层应拒绝启动：%v", err)
	}

	h = newHub()
	h.AddLayer(authLayer())
	_ = h.Register(itemTool("write_file", "fs.nonesuch"))
	if err := h.Validate(); err == nil || !strings.Contains(err.Error(), "未注册的权限项 fs.nonesuch") {
		t.Fatalf("引用未注册项应报错：%v", err)
	}

	h = newHub()
	h.AddLayer(authLayer())
	bad := itemTool("todo", "fs.write")
	bad.NoAuth = true
	_ = h.Register(bad)
	if err := h.Validate(); err == nil || !strings.Contains(err.Error(), "既声明无需授权") {
		t.Fatalf("NoAuth 又引用项应报错：%v", err)
	}

	// 未声明权限项也不报错：按“一律询问”处理。
	h = newHub()
	h.AddLayer(authLayer())
	_ = h.Register(itemTool("mystery"))
	if err := h.Validate(); err != nil {
		t.Fatalf("未声明权限项的工具不应阻止启动：%v", err)
	}
}

// 可见性：deny 的工具不进显露集但仍在可用集；内置工具不受影响；只能设置一次。
func TestVisibility(t *testing.T) {
	h := New(Options{})
	_ = h.Register(itemTool("write_file"))
	_ = h.Register(itemTool("shell"))
	if err := h.SetVisible(func(n string) bool { return n != "shell" }); err != nil {
		t.Fatal(err)
	}
	if err := h.SetVisible(func(string) bool { return true }); err == nil {
		t.Fatal("可见性判定应只允许设置一次")
	}
	ts := h.SpecsFor(nil, 0)
	names := map[string]bool{}
	for _, s := range h.Exposed(ts) {
		names[s.Name] = true
	}
	if !names["write_file"] || names["shell"] {
		t.Fatalf("deny 的工具不应显露：%v", names)
	}
	avail := strings.Join(ts.Available(), ",")
	if !strings.Contains(avail, "shell") {
		t.Fatalf("deny 的工具仍应留在可用集：%s", avail)
	}
}

// 缺省授权层（未装权限组件时用，审查 P0-7）：只读、声明无需授权的工具放行；有副作用的要确认，确认后放行。
func TestDefaultAuthorizer(t *testing.T) {
	ran := map[string]int{}
	mk := func(name string, risk Risk, noAuth bool) *Tool {
		return &Tool{Spec: spec(name), Risk: risk, NoAuth: noAuth, Call: func(context.Context, *Cx, json.RawMessage) (Output, error) {
			ran[name]++
			return Text("ok"), nil
		}}
	}
	h := New(Options{})
	for _, tl := range []*Tool{mk("r", Read, false), mk("w", Write, false), mk("n", Write, true)} {
		if err := h.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	h.AddLayer(DefaultAuthorizer())
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
	rs := drain(h.Execute(context.Background(), kctx("s"), []session.ToolCall{call("1", "r", `{}`), call("2", "n", `{}`), call("3", "w", `{}`)}, nil))
	if ran["r"] != 1 || ran["n"] != 1 {
		t.Fatalf("只读与无需授权的工具应放行: %v", ran)
	}
	if ran["w"] != 0 || rs[2].Output.Kind == Done && rs[2].Output.Status == session.ToolOK {
		t.Fatalf("写工具未确认不应执行: ran=%v out=%+v", ran, rs[2].Output)
	}
}
