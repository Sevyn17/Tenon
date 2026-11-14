package wire

// 声明式视图契约的测试。契约在 declarative.go，这里是它的护栏。
//
// 为什么这些断言值得写：`UISlot.View` 是 json.RawMessage，框架原本只校验大小与
// 顶层 type，节点内部字段没有任何运行时校验点。2026-11-14 就因此错了四处字段名
// （code 写成 text、keyValue 写成 keyValue、button/link 写成 text），照契约写出的
// 插件视图在应用里渲染成空，两个仓库都没有测试能发现。
//
// 三条护栏，分别钉不同的东西：
//   - TestDeclKindsMatchStruct：declKinds 表里出现的字段必须真的在 DeclNode 上
//     （防止表与结构体漂移）；
//   - TestValidateView：校验器按表工作（必填缺失要报错、未知节点要放行）；
//   - TestDeclNodeTSShape：生成的 TS 联合类型与表一致（渲染器拿到的类型是对的）。

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestDeclKindsMatchStruct declKinds 表引用的每个字段都必须是 DeclNode 上真实存在的
// JSON 字段，且不能重复。表的字段名写错会让校验与 TS 生成同时静默失效。
func TestDeclKindsMatchStruct(t *testing.T) {
	known := map[string]bool{}
	typ := reflect.TypeOf(DeclNode{})
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			known[name] = true
		}
	}
	seenType := map[string]bool{}
	for _, k := range declKinds {
		if k.Type == "" || seenType[k.Type] {
			t.Fatalf("节点类型为空或重复：%q", k.Type)
		}
		seenType[k.Type] = true
		fields := map[string]bool{}
		for _, f := range append(append([]string{}, k.Required...), k.Optional...) {
			if fields[f] {
				t.Fatalf("%s 的字段 %q 同时出现在必填与可选里", k.Type, f)
			}
			fields[f] = true
			if !known[f] {
				t.Errorf("%s 引用了 DeclNode 上不存在的 JSON 字段 %q", k.Type, f)
			}
			if _, ok := k.Override[f]; ok && !known[f] {
				t.Errorf("%s 的 Override 指向不存在的字段 %q", k.Type, f)
			}
		}
		for f := range k.Override {
			if !fields[f] {
				t.Errorf("%s 的 Override 字段 %q 不在 Required/Optional 里（写了也不会生效）", k.Type, f)
			}
		}
	}
	if len(DeclNodeTypes()) != len(declKinds) {
		t.Fatalf("DeclNodeTypes（%d）与 declKinds（%d）数量不一致", len(DeclNodeTypes()), len(declKinds))
	}
}

// TestValidateView 校验器按表工作：必填缺失报错，未知节点放行。
func TestValidateView(t *testing.T) {
	ok := []string{
		`{"type":"divider"}`,
		`{"type":"text","text":"你好"}`,
		`{"type":"text","text":"你好","tone":"muted"}`,
		`{"type":"code","code":"x := 1","lang":"go"}`,
		`{"type":"keyValue","rows":[["k","v"]]}`,
		`{"type":"table","columns":["a"],"rows":[["b"]]}`,
		`{"type":"progress","value":0.5}`,
		`{"type":"button","label":"点","action":{"kind":"insert","text":"x"}}`,
		`{"type":"form","schema":{},"submit":"存","action":{"kind":"call","cmd":"x"}}`,
		`{"type":"stack","children":[{"type":"text","text":"嵌套"}]}`,
		`{"type":"list","items":[{"type":"divider"}]}`,
		`[{"type":"text","text":"a"},{"type":"divider"}]`,
		// 未知节点：渲染器占位，不算错（前向兼容）。
		`{"type":"future-node","whatever":1}`,
	}
	for _, raw := range ok {
		if err := ValidateView([]byte(raw)); err != nil {
			t.Errorf("应通过却被拒：%s → %v", raw, err)
		}
	}

	bad := []struct{ raw, why string }{
		{`{"type":"text"}`, "text 缺 text"},
		{`{"type":"code"}`, "code 缺 code"},
		{`{"type":"code","text":"x"}`, "code 用了 text（历史错误写法）"},
		{`{"type":"keyValue"}`, "keyValue 缺 rows"},
		{`{"type":"keyValue","keyValue":[["k","v"]]}`, "keyValue 用了 keyValue（历史错误写法）"},
		{`{"type":"button"}`, "button 缺 label"},
		{`{"type":"button","text":"点","action":{"kind":"insert","text":"x"}}`, "button 用了 text（历史错误写法）"},
		{`{"type":"button","label":"点"}`, "button 缺 action"},
		{`{"type":"button","label":"点","action":{"kind":"bogus"}}`, "action.kind 不合法"},
		{`{"type":"table","columns":["a"]}`, "table 缺 rows"},
		{`{"type":"progress"}`, "progress 缺 value"},
		{`{"type":"stack"}`, "stack 缺 children"},
		{`{"type":"stack","children":[{"type":"text"}]}`, "嵌套节点缺必填"},
		{`{"type":""}`, "type 为空"},
		{`{}`, "缺 type"},
		{`"不是对象"`, "不是节点"},
		{`[]`, "空数组"},
	}
	for _, c := range bad {
		if err := ValidateView([]byte(c.raw)); err == nil {
			t.Errorf("应被拒却通过（%s）：%s", c.why, c.raw)
		}
	}
}

// TestDeclNodeTSShape 生成的 TS 联合类型与表一致。
func TestDeclNodeTSShape(t *testing.T) {
	ts := declNodeTS()
	for _, want := range []string{
		`type: "stack"; children: DeclNode[]; gap?: number`,
		`type: "code"; code: string; lang?: string`,
		`type: "keyValue"; rows: [string, string][]`,
		`type: "table"; columns: string[]; rows: (string | number)[][]`,
		`type: "progress"; value: number; label?: string`,
		`type: "button"; label: string; action: DeclAction; variant?: string`,
		`type: "divider"`,
	} {
		if !strings.Contains(ts, want) {
			t.Errorf("生成的 DeclNode 缺 %q\n实际：\n%s", want, ts)
		}
	}
	// 每个节点种类都要出现一次。
	for _, typ := range DeclNodeTypes() {
		if !strings.Contains(ts, `type: "`+typ+`"`) {
			t.Errorf("生成的 DeclNode 缺节点种类 %q", typ)
		}
	}
	if strings.Count(ts, "| {") != len(declKinds) {
		t.Errorf("DeclNode 的联合分支数（%d）与 declKinds（%d）不一致",
			strings.Count(ts, "| {"), len(declKinds))
	}
}

// TestDeclActionTSShape 动作联合类型的四种 kind 都在。
func TestDeclActionTSShape(t *testing.T) {
	ts := declActionTS()
	for _, k := range DeclActionKinds() {
		if !strings.Contains(ts, `kind: "`+k+`"`) {
			t.Errorf("DeclAction 缺动作种类 %q", k)
		}
	}
}

// TestGeneratedTSConsistent wire.gen.ts 里的 DeclNode 必须与当前表生成的一致。
// 这条把「改了 Go 忘了重新生成」变成测试失败。
func TestGeneratedTSConsistent(t *testing.T) {
	got := TypeScript()
	for _, want := range []string{declActionTS(), declNodeTS()} {
		if !strings.Contains(got, want) {
			t.Errorf("TypeScript() 没包含契约生成的类型：\n%s", want)
		}
	}
}

// TestDeclKindJSONRoundTrip 每种节点的最小 JSON 都能解成 DeclNode 且字段不丢。
// 与 ValidateView 互补：前者查必填，这条查「解出来的字段真的在」。
func TestDeclKindJSONRoundTrip(t *testing.T) {
	cases := map[string]string{
		"stack":    `{"type":"stack","gap":3,"children":[{"type":"divider"}]}`,
		"row":      `{"type":"row","gap":2,"align":"end","children":[{"type":"divider"}]}`,
		"text":     `{"type":"text","text":"你好","tone":"muted","size":"sm","bold":true}`,
		"markdown": `{"type":"markdown","text":"# 标题"}`,
		"badge":    `{"type":"badge","text":"新","tone":"accent"}`,
		"code":     `{"type":"code","code":"x := 1","lang":"go"}`,
		"diff":     `{"type":"diff","diff":"--- a\n+++ b\n"}`,
		"list":     `{"type":"list","ordered":true,"items":[{"type":"text","text":"a"}]}`,
		"table":    `{"type":"table","columns":["名","值"],"rows":[["a",1]]}`,
		"keyValue": `{"type":"keyValue","rows":[["键","值"]]}`,
		"progress": `{"type":"progress","value":0.5,"label":"进度"}`,
		"button":   `{"type":"button","label":"执行","variant":"primary","action":{"kind":"insert","text":"x"}}`,
		"link":     `{"type":"link","label":"开面板","action":{"kind":"openPanel","panel":"p1"}}`,
		"form":     `{"type":"form","schema":{"type":"object"},"value":{"a":1},"submit":"提交","action":{"kind":"respond","interaction":"i1","response":{}}}`,
		"divider":  `{"type":"divider"}`,
	}
	if len(cases) != len(declKinds) {
		t.Fatalf("用例覆盖 %d 种节点，declKinds 有 %d 种", len(cases), len(declKinds))
	}
	for typ, raw := range cases {
		var n DeclNode
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			t.Errorf("%s 解析失败：%v", typ, err)
			continue
		}
		if n.Type != typ {
			t.Errorf("%s 解出的 Type 是 %q", typ, n.Type)
		}
		if err := ValidateView([]byte(raw)); err != nil {
			t.Errorf("%s 的最小用例没通过校验：%v", typ, err)
		}
	}
}
