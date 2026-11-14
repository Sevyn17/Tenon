package wire

// 声明式视图的形状（`[[ui]]` 段 view / view_file 的内容）。
//
// 这是**唯一事实源**：插件作者照这里的 JSON 写视图，应用的渲染器用这里生成的
// TS 类型（web/src/wire.gen.ts 的 DeclNode / DeclAction），框架在协议边界按
// declKinds 表校验。三处同源。
//
// 为什么放在 wire 而不是 runtime/pkgs：视图是 `UISlot.View` 的载荷，跟 Entry、
// UISlot 一样是线上类型，应该走同一条生成链（tsgen）。放在 pkgs 时它是全框架
// 唯一一份「手写两份、两边无法互相校验」的契约——2026-11-14 就因此错了四处
// 字段名（code 写成 text、keyValue 写成 keyValue、button/link 写成 text），
// 照契约写出的插件视图在应用里渲染成空，且没有任何测试能发现。
//
// 节点种类刻意保持小而稳定：只描述内容与有限的几种动作，不允许执行任意代码。
// 未知节点由渲染器显示占位，不影响整块视图（所以校验**不拒绝**未知 type）。

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// DeclAction 声明式视图里按钮 / 链接能触发的动作。由应用决定怎么执行。
//
// 种类固定为四种；应用实现不了的动作应当把按钮显示为不可用，
// 不要出现「点了没反应」。
type DeclAction struct {
	// Kind 动作种类：call / openPanel / insert / respond。
	Kind string `json:"kind"`
	// Cmd Kind=call：要调用的协议命令名（如 "session.stats"）。
	Cmd string `json:"cmd,omitempty"`
	// Args Kind=call：命令参数。
	Args map[string]any `json:"args,omitempty"`
	// Panel Kind=openPanel：要打开的面板名。
	Panel string `json:"panel,omitempty"`
	// Params Kind=openPanel：面板参数。
	Params map[string]any `json:"params,omitempty"`
	// Text Kind=insert：要填进输入框的文本。
	Text string `json:"text,omitempty"`
	// Interaction Kind=respond：要回答的交互 id。
	Interaction string `json:"interaction,omitempty"`
	// Response Kind=respond：回答内容。
	Response any `json:"response,omitempty"`
}

// DeclNode 声明式视图的一个节点。渲染器按 Type 分派。
//
// 字段是**扁平**的（所有节点共用一套字段，按 Type 取用），各节点用哪些字段见
// declKinds 表。三处同一 JSON 字段承载两种语义是契约的一部分（渲染器按 Type
// 分别处理），所以 Go 侧也共用一个字段：
//
//	label  progress 的说明文字 / button、link 的文字
//	rows   table 的行（每行是单元格数组）/ keyValue 的键值对（每行是 [键, 值]）
//	value  progress 的进度（0–1）/ form 的初始值（对象）
type DeclNode struct {
	// Type 节点种类，取值见 DeclNodeTypes。
	Type string `json:"type"`

	// —— 容器：stack / row ——
	Children []DeclNode `json:"children,omitempty"` // 子节点
	Gap      int        `json:"gap,omitempty"`      // 间距
	Align    string     `json:"align,omitempty"`    // row 的水平对齐：start / center / end

	// —— 文本：text / markdown / badge ——
	Text string `json:"text,omitempty"` // 正文
	Tone string `json:"tone,omitempty"` // 色调；词表按节点不同，见 declKinds
	Size string `json:"size,omitempty"` // text 的字号：sm / md / lg
	Bold bool   `json:"bold,omitempty"` // text 是否加粗

	// —— 代码：code / diff ——
	Code string `json:"code,omitempty"` // code 的正文
	Lang string `json:"lang,omitempty"` // code 的语言标记
	Diff string `json:"diff,omitempty"` // diff 的统一格式差异文本

	// —— 列表：list / table / keyValue ——
	Items   []DeclNode `json:"items,omitempty"`   // list 的条目
	Ordered bool       `json:"ordered,omitempty"` // list 是否有序
	Columns []string   `json:"columns,omitempty"` // table 的列名
	// Rows table 的行 / keyValue 的键值对，见类型注释。
	Rows [][]any `json:"rows,omitempty"`

	// —— 进度：progress ——
	// Value 语义随 Type 变：progress 时是进度（0–1）；form 时是初始值。
	// 两者共用 JSON 字段名 `value`，所以 Go 侧也共用一个字段，用 any 承载。
	Value any `json:"value,omitempty"`

	// —— 交互：button / link / form ——
	// Label 文字：progress 的说明 / button、link 的按钮文字，见类型注释。
	Label   string         `json:"label,omitempty"`
	Action  *DeclAction    `json:"action,omitempty"`  // 点击后的动作
	Variant string         `json:"variant,omitempty"` // button 的样式：default / primary / ghost / danger / outline
	Schema  map[string]any `json:"schema,omitempty"`  // form 的 JSON Schema
	Submit  string         `json:"submit,omitempty"`  // form 的提交按钮文字
}

// declKind 一种节点的字段约定。这张表同时驱动三件事：
//   - 校验（ValidateView 按 Required 检查）
//   - TS 类型生成（tsgen 按 Required/Optional 生成可辨识联合）
//   - 文档（DeclNodeTypes 由这张表派生）
//
// TS 里每个字段的类型默认从 DeclNode 的 Go 类型推（见 declFieldTS），
// Override 只用于 Go 侧是 any、线上语义更窄的字段。
type declKind struct {
	Type     string
	Required []string
	Optional []string
	Override map[string]string
}

// declKinds 全部节点种类与各自用到的 JSON 字段。
//
// tone 的词表按节点不同，不要混用：
//
//	text   default / muted / danger / success
//	badge  neutral / accent / success / danger / warning
var declKinds = []declKind{
	{Type: "stack", Required: []string{"children"}, Optional: []string{"gap"}},
	{Type: "row", Required: []string{"children"}, Optional: []string{"gap", "align"}},
	{Type: "text", Required: []string{"text"}, Optional: []string{"tone", "size", "bold"}},
	{Type: "markdown", Required: []string{"text"}},
	{Type: "badge", Required: []string{"text"}, Optional: []string{"tone"}},
	{Type: "code", Required: []string{"code"}, Optional: []string{"lang"}},
	{Type: "diff", Required: []string{"diff"}},
	{Type: "list", Required: []string{"items"}, Optional: []string{"ordered"}},
	{Type: "table", Required: []string{"columns", "rows"},
		Override: map[string]string{"rows": "(string | number)[][]"}},
	{Type: "keyValue", Required: []string{"rows"},
		Override: map[string]string{"rows": "[string, string][]"}},
	{Type: "progress", Required: []string{"value"}, Optional: []string{"label"},
		Override: map[string]string{"value": "number"}},
	{Type: "button", Required: []string{"label", "action"}, Optional: []string{"variant"},
		Override: map[string]string{"action": "DeclAction"}},
	{Type: "link", Required: []string{"label", "action"},
		Override: map[string]string{"action": "DeclAction"}},
	{Type: "form", Required: []string{"schema", "submit", "action"}, Optional: []string{"value"},
		Override: map[string]string{"action": "DeclAction", "value": "Record<string, unknown>"}},
	{Type: "divider"},
}

// DeclNodeTypes 合法的节点种类（由 declKinds 派生，不要单独维护）。
// 返回副本：调用方改不动契约。
func DeclNodeTypes() []string {
	out := make([]string, len(declKinds))
	for i, k := range declKinds {
		out[i] = k.Type
	}
	return out
}

// DeclActionKinds 合法的动作种类。返回副本。
func DeclActionKinds() []string {
	return append([]string(nil), declActionKinds...)
}

// declActionKinds 动作种类的唯一清单。
var declActionKinds = []string{"call", "openPanel", "insert", "respond"}

// declKindOf 按类型名找字段约定。
func declKindOf(typ string) (declKind, bool) {
	for _, k := range declKinds {
		if k.Type == typ {
			return k, true
		}
	}
	return declKind{}, false
}

// ValidateView 校验一段视图 JSON 是否符合声明式视图契约。
//
// 视图可以是单个节点，也可以是节点数组。规则：
//   - 未知 type **不报错**：渲染器会显示占位，前向兼容比严格更重要；
//   - 已知 type 必须带齐 Required 字段；
//   - children / items 递归校验；带 action 的节点校验动作种类。
//
// 直接校验原始 JSON 而不是解成 DeclNode 再查：Go 的零值（false、0、""）与
// 「字段没出现」无法区分，而契约关心的是后者。
func ValidateView(raw []byte) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("视图不是合法 JSON：%w", err)
	}
	switch x := v.(type) {
	case map[string]any:
		return validateNode(x, "")
	case []any:
		if len(x) == 0 {
			return fmt.Errorf("视图数组为空")
		}
		for i, n := range x {
			m, ok := n.(map[string]any)
			if !ok {
				return fmt.Errorf("第 %d 个节点不是对象", i+1)
			}
			if err := validateNode(m, fmt.Sprintf("%d.", i+1)); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("视图须是带 type 的节点或节点数组")
	}
}

// validateNode 校验单个节点。path 是报错用的位置前缀（如 "2.children.0."）。
func validateNode(n map[string]any, path string) error {
	where := "节点"
	if path != "" {
		where = "第 " + strings.TrimSuffix(path, ".") + " 个节点"
	}
	typ, _ := n["type"].(string)
	if typ == "" {
		return fmt.Errorf("%s缺 type", where)
	}
	k, known := declKindOf(typ)
	if !known {
		return nil // 未知节点：渲染器占位，不算错
	}
	for _, f := range k.Required {
		if _, ok := n[f]; !ok {
			return fmt.Errorf("%s（%s）缺必填字段 %q", where, typ, f)
		}
	}
	if a, ok := n["action"]; ok && a != nil {
		am, ok := a.(map[string]any)
		if !ok {
			return fmt.Errorf("%s（%s）的 action 不是对象", where, typ)
		}
		kind, _ := am["kind"].(string)
		if !isDeclActionKind(kind) {
			return fmt.Errorf("%s（%s）的 action.kind %q 不合法（合法：%s）",
				where, typ, kind, strings.Join(declActionKinds, " / "))
		}
	}
	if err := validateChildren(n, "children", path, where, typ); err != nil {
		return err
	}
	return validateChildren(n, "items", path, where, typ)
}

// validateChildren 递归校验 children / items 里的子节点。
func validateChildren(n map[string]any, key, path, where, typ string) error {
	raw, ok := n[key]
	if !ok || raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("%s（%s）的 %s 不是数组", where, typ, key)
	}
	for i, c := range list {
		m, ok := c.(map[string]any)
		if !ok {
			return fmt.Errorf("%s（%s）的 %s[%d] 不是对象", where, typ, key, i)
		}
		if err := validateNode(m, fmt.Sprintf("%s%s.%d.", path, key, i)); err != nil {
			return err
		}
	}
	return nil
}

func isDeclActionKind(k string) bool {
	for _, v := range declActionKinds {
		if v == k {
			return true
		}
	}
	return false
}

// declFieldTS JSON 字段名 → TS 类型。默认从 DeclNode 的 Go 类型推，
// Override 里的优先。生成器用它产出可辨识联合。
func declFieldTS(field string, override map[string]string) string {
	if t, ok := override[field]; ok {
		return t
	}
	t := reflect.TypeOf(DeclNode{})
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		name, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
		if name == field {
			return tsOf(sf.Type, map[reflect.Type]bool{reflect.TypeOf(DeclNode{}): true})
		}
	}
	return "unknown"
}

// declActionTS DeclAction 的 TS 可辨识联合（按 kind 分派）。生成器内部用，不导出：
// 它只是 tsgen 的实现细节，不该成为协议公开面。
func declActionTS() string {
	return `export type DeclAction =
  | { kind: "call"; cmd: string; args?: unknown }
  | { kind: "openPanel"; panel: string; params?: unknown }
  | { kind: "insert"; text: string }
  | { kind: "respond"; interaction: string; response: unknown };`
}

// declNodeTS DeclNode 的 TS 可辨识联合，由 declKinds 生成。生成器内部用，不导出。
func declNodeTS() string {
	var b strings.Builder
	b.WriteString("export type DeclNode =\n")
	for i, k := range declKinds {
		fields := make([]string, 0, 1+len(k.Required)+len(k.Optional))
		fields = append(fields, fmt.Sprintf("type: %q", k.Type))
		for _, f := range k.Required {
			fields = append(fields, fmt.Sprintf("%s: %s", f, declFieldTS(f, k.Override)))
		}
		for _, f := range k.Optional {
			fields = append(fields, fmt.Sprintf("%s?: %s", f, declFieldTS(f, k.Override)))
		}
		b.WriteString("  | { " + strings.Join(fields, "; ") + " }")
		if i < len(declKinds)-1 {
			b.WriteString("\n")
		} else {
			b.WriteString(";")
		}
	}
	return b.String()
}
