package pkgs

// 声明式视图的形状契约（declarative.go）：钉住**线上 JSON 字段名**。
//
// 为什么单独一条：`UISlot.View` 是 json.RawMessage，框架侧只校验大小与顶层 type，
// 节点内部的字段名没有任何运行时校验点。契约写错不会编译失败、也不会让任何测试变红，
// 只会让照契约写出的插件视图在应用里渲染成空白（2026-11-14 就发生过：
// code/keyValue/button/link 的字段名与渲染器差了一轮，两边都没发现）。
// 所以这里按「渲染器读什么」逐节点给最小 JSON，反序列化后断言关键字段非零。
//
// 改这些断言等于改对外契约：必须同时改 declarative.go 与应用的渲染器。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// decodeNode 把一段视图 JSON 解成 DeclNode。视图可以是单个节点，也可以是节点数组；
// 数组时取第一个（契约允许顶层是数组，见 checkViewShape）。
func decodeNode(t *testing.T, raw string) DeclNode {
	t.Helper()
	var one DeclNode
	if err := json.Unmarshal([]byte(raw), &one); err == nil && one.Type != "" {
		return one
	}
	var many []DeclNode
	if err := json.Unmarshal([]byte(raw), &many); err != nil {
		t.Fatalf("视图既不是节点也不是节点数组：%s（%v）", raw, err)
	}
	if len(many) == 0 {
		t.Fatalf("视图数组为空：%s", raw)
	}
	return many[0]
}

// TestDeclNodeFieldNames 每种节点用一段最小 JSON 走一遍，断言渲染器要读的字段都解出来了。
// 键名写错（比如把 code 的正文写成 text）会让对应的断言落空。
func TestDeclNodeFieldNames(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		check func(t *testing.T, n DeclNode)
	}{
		{
			name: "stack",
			raw:  `{"type":"stack","gap":3,"children":[{"type":"divider"}]}`,
			check: func(t *testing.T, n DeclNode) {
				if n.Gap != 3 || len(n.Children) != 1 || n.Children[0].Type != "divider" {
					t.Fatalf("stack：%+v", n)
				}
			},
		},
		{
			name: "row",
			raw:  `{"type":"row","gap":2,"align":"end","children":[{"type":"divider"}]}`,
			check: func(t *testing.T, n DeclNode) {
				if n.Gap != 2 || n.Align != "end" || len(n.Children) != 1 {
					t.Fatalf("row：%+v", n)
				}
			},
		},
		{
			name: "text",
			raw:  `{"type":"text","text":"你好","tone":"muted","size":"sm","bold":true}`,
			check: func(t *testing.T, n DeclNode) {
				if n.Text != "你好" || n.Tone != "muted" || n.Size != "sm" || !n.Bold {
					t.Fatalf("text：%+v", n)
				}
			},
		},
		{
			name: "markdown",
			raw:  `{"type":"markdown","text":"# 标题"}`,
			check: func(t *testing.T, n DeclNode) {
				if n.Text != "# 标题" {
					t.Fatalf("markdown：%+v", n)
				}
			},
		},
		{
			name: "badge",
			raw:  `{"type":"badge","text":"新","tone":"accent"}`,
			check: func(t *testing.T, n DeclNode) {
				// badge 的 tone 用 badge 词表（neutral/accent/success/danger/warning），不是 text 那套。
				if n.Text != "新" || n.Tone != "accent" {
					t.Fatalf("badge：%+v", n)
				}
			},
		},
		{
			name: "code",
			raw:  `{"type":"code","code":"fmt.Println(1)","lang":"go"}`,
			check: func(t *testing.T, n DeclNode) {
				// 正文在 code 字段上，不是 text。
				if n.Code != "fmt.Println(1)" || n.Lang != "go" {
					t.Fatalf("code：%+v", n)
				}
				if n.Text != "" {
					t.Fatalf("code 的正文不该落在 text 上：%+v", n)
				}
			},
		},
		{
			name: "diff",
			raw:  `{"type":"diff","diff":"--- a\n+++ b\n"}`,
			check: func(t *testing.T, n DeclNode) {
				if n.Diff != "--- a\n+++ b\n" {
					t.Fatalf("diff：%+v", n)
				}
			},
		},
		{
			name: "list",
			raw:  `{"type":"list","ordered":true,"items":[{"type":"text","text":"a"}]}`,
			check: func(t *testing.T, n DeclNode) {
				if !n.Ordered || len(n.Items) != 1 || n.Items[0].Text != "a" {
					t.Fatalf("list：%+v", n)
				}
			},
		},
		{
			name: "table",
			raw:  `{"type":"table","columns":["名","值"],"rows":[["a",1]]}`,
			check: func(t *testing.T, n DeclNode) {
				if len(n.Columns) != 2 || len(n.Rows) != 1 || len(n.Rows[0]) != 2 {
					t.Fatalf("table：%+v", n)
				}
			},
		},
		{
			name: "keyValue",
			raw:  `{"type":"keyValue","rows":[["键","值"]]}`,
			check: func(t *testing.T, n DeclNode) {
				// 键值对也在 rows 上（与 table 共用字段），不是 keyValue。
				if len(n.Rows) != 1 || len(n.Rows[0]) != 2 || n.Rows[0][0] != "键" || n.Rows[0][1] != "值" {
					t.Fatalf("keyValue：%+v", n)
				}
			},
		},
		{
			name: "progress",
			raw:  `{"type":"progress","value":0.5,"label":"进度"}`,
			check: func(t *testing.T, n DeclNode) {
				if v, ok := n.Value.(float64); !ok || v != 0.5 {
					t.Fatalf("progress 的 value：%#v", n.Value)
				}
				if n.Label != "进度" {
					t.Fatalf("progress 的 label：%+v", n)
				}
			},
		},
		{
			name: "button",
			raw:  `{"type":"button","label":"执行","variant":"primary","action":{"kind":"insert","text":"x"}}`,
			check: func(t *testing.T, n DeclNode) {
				// 按钮文字在 label 上，不是 text。
				if n.Label != "执行" || n.Variant != "primary" {
					t.Fatalf("button：%+v", n)
				}
				if n.Action == nil || n.Action.Kind != "insert" || n.Action.Text != "x" {
					t.Fatalf("button 的 action：%+v", n.Action)
				}
			},
		},
		{
			name: "link",
			raw:  `{"type":"link","label":"开面板","action":{"kind":"openPanel","panel":"p1"}}`,
			check: func(t *testing.T, n DeclNode) {
				if n.Label != "开面板" {
					t.Fatalf("link：%+v", n)
				}
				if n.Action == nil || n.Action.Kind != "openPanel" || n.Action.Panel != "p1" {
					t.Fatalf("link 的 action：%+v", n.Action)
				}
			},
		},
		{
			name: "form",
			raw:  `{"type":"form","schema":{"type":"object"},"value":{"a":1},"submit":"提交","action":{"kind":"respond","interaction":"i1","response":{"a":1}}}`,
			check: func(t *testing.T, n DeclNode) {
				if n.Schema == nil || n.Submit != "提交" {
					t.Fatalf("form：%+v", n)
				}
				// form 的 value 是对象（progress 是数字），同一个 JSON 字段。
				if v, ok := n.Value.(map[string]any); !ok || v["a"] != float64(1) {
					t.Fatalf("form 的 value：%#v", n.Value)
				}
				if n.Action == nil || n.Action.Kind != "respond" || n.Action.Interaction != "i1" {
					t.Fatalf("form 的 action：%+v", n.Action)
				}
			},
		},
		{
			name: "divider",
			raw:  `{"type":"divider"}`,
			check: func(t *testing.T, n DeclNode) {
				if n.Type != "divider" {
					t.Fatalf("divider：%+v", n)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := decodeNode(t, c.raw)
			if n.Type != c.name {
				t.Fatalf("Type = %q，想要 %q", n.Type, c.name)
			}
			c.check(t, n)
		})
	}
}

// TestDeclActionFieldNames 四种动作各自的字段名。
func TestDeclActionFieldNames(t *testing.T) {
	cases := []struct {
		raw   string
		check func(t *testing.T, a DeclAction)
	}{
		{
			raw: `{"kind":"call","cmd":"todo.list","args":{"all":true}}`,
			check: func(t *testing.T, a DeclAction) {
				if a.Cmd != "todo.list" || a.Args["all"] != true {
					t.Fatalf("call：%+v", a)
				}
			},
		},
		{
			raw: `{"kind":"openPanel","panel":"p1","params":{"x":1}}`,
			check: func(t *testing.T, a DeclAction) {
				if a.Panel != "p1" || a.Params["x"] != float64(1) {
					t.Fatalf("openPanel：%+v", a)
				}
			},
		},
		{
			raw: `{"kind":"insert","text":"填入"}`,
			check: func(t *testing.T, a DeclAction) {
				if a.Text != "填入" {
					t.Fatalf("insert：%+v", a)
				}
			},
		},
		{
			raw: `{"kind":"respond","interaction":"i1","response":{"ok":true}}`,
			check: func(t *testing.T, a DeclAction) {
				if a.Interaction != "i1" || a.Response == nil {
					t.Fatalf("respond：%+v", a)
				}
			},
		},
	}
	for _, c := range cases {
		var a DeclAction
		if err := json.Unmarshal([]byte(c.raw), &a); err != nil {
			t.Fatalf("%s：%v", c.raw, err)
		}
		if a.Kind == "" {
			t.Fatalf("%s：kind 没解出来", c.raw)
		}
		c.check(t, a)
	}
}

// TestDeclKindsMatchList 契约里声明的种类列表与实际能解出来的节点/动作种类一致，
// 防止「列表加了新种类但字段没跟上」或反过来。
func TestDeclKindsMatchList(t *testing.T) {
	if got := len(DeclNodeTypes); got != 15 {
		t.Fatalf("DeclNodeTypes 有 %d 种，契约文档写的是 15 种", got)
	}
	if got := len(DeclActionKinds); got != 4 {
		t.Fatalf("DeclActionKinds 有 %d 种，契约文档写的是 4 种", got)
	}
	seen := map[string]bool{}
	for _, ty := range DeclNodeTypes {
		if ty == "" || seen[ty] {
			t.Fatalf("DeclNodeTypes 有空项或重复：%v", DeclNodeTypes)
		}
		seen[ty] = true
	}
	seenA := map[string]bool{}
	for _, k := range DeclActionKinds {
		if k == "" || seenA[k] {
			t.Fatalf("DeclActionKinds 有空项或重复：%v", DeclActionKinds)
		}
		seenA[k] = true
	}
}

// TestUISectionViewShape 走一遍真实的 [[ui]] 解析，确认契约里的节点能进 UISlot.View 再解回来。
// 这条把「解析层」和「形状契约」串起来：解析层只认顶层 type，节点内部字段靠契约。
func TestUISectionViewShape(t *testing.T) {
	root := t.TempDir()
	dir := mk(t, root, "decl-pkg", `name = "decl-pkg"

[[ui]]
id = "demo"
slot = "conversation.card"
view_file = "ui/view.json"
`)
	if err := os.MkdirAll(filepath.Join(dir, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui", "view.json"), []byte(`[
		{"type":"code","code":"x := 1","lang":"go"},
		{"type":"keyValue","rows":[["k","v"]]},
		{"type":"button","label":"点我","action":{"kind":"call","cmd":"session.stats"}}
	]`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Parse(dir, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	us := p.UISlots()
	if len(us) != 1 {
		t.Fatalf("解析出 %d 条 [[ui]]", len(us))
	}
	n := decodeNode(t, string(us[0].View))
	if n.Type != "code" || n.Code != "x := 1" || n.Lang != "go" {
		t.Fatalf("view 的第一个节点解错：%+v", n)
	}
}
