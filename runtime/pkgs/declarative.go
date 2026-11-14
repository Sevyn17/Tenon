package pkgs

// 声明式视图的形状（`[[ui]]` 段 `view` / `view_file` 的内容，2026-11-13）。
//
// 这是框架对插件包界面贡献的**契约**：插件不写前端代码，用一段 JSON 描述界面，
// 由应用的界面层渲染。框架只定义形状与大小上限，不提供渲染器——
// 渲染器是应用层的东西（如 Mortise 的 `client/web/src/ui/declarative.tsx`）。
//
// 为什么形状要留在框架：`UISlot.View` 是 `json.RawMessage`，只校验大小不校验形状的话，
// 「插件能声明界面」这个扩展点就是有定义、无约束的空承诺。有了这里的定义，
// 第三方插件知道该写什么，应用的渲染器也知道该认哪些节点。
//
// 节点种类刻意保持小而稳定：只描述内容与有限的几种动作，不允许执行任意代码。
// 未知节点由渲染器显示占位，不影响整块视图。
//
// **2026-11-14 订正**：本文件初版（7c48b50）是渲染器迁出之后凭描述转写的，
// 有四处 JSON 字段名与渲染器实际读的不一致（code 用了 text、keyValue 用了 keyValue、
// button/link 用了 text、tone 词表按 text 的一套写），照契约写出的视图在应用里会渲染成空。
// 现在以渲染器实现为准逐字段对齐，并由 `declarative_test.go` 钉住 JSON 字段名。
// 改这里的 JSON 标签就是改对外契约，必须同时改渲染器与那条测试。

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
// 各节点种类用哪些字段见 DeclNodeTypes 旁的说明；不相关的字段在 JSON 里不出现。
//
// 字段名按「渲染器读什么」定，不按 Go 侧的语义定。三处**同一 JSON 字段承载两种语义**
// 是契约的一部分（渲染器按 Type 分别处理），所以 Go 侧也共用一个字段：
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
	Tone string `json:"tone,omitempty"` // 色调；词表按节点不同，见 DeclNodeTypes
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
	// 两者共用 JSON 字段名 `value`（契约如此，渲染器按 Type 分别处理），
	// 所以 Go 侧也共用一个字段，用 any 承载两种类型。
	Value any `json:"value,omitempty"`

	// —— 交互：button / link / form ——
	// Label 文字：progress 的说明 / button、link 的按钮文字，见类型注释。
	Label   string         `json:"label,omitempty"`
	Action  *DeclAction    `json:"action,omitempty"`  // 点击后的动作
	Variant string         `json:"variant,omitempty"` // button 的样式：default / primary / ghost / danger / outline
	Schema  map[string]any `json:"schema,omitempty"`  // form 的 JSON Schema
	Submit  string         `json:"submit,omitempty"`  // form 的提交按钮文字
}

// DeclNodeTypes 合法的节点种类，以及每种用到的 JSON 字段：
//
//	stack     children, gap
//	row       children, gap, align（start / center / end）
//	text      text, tone, size, bold
//	markdown  text
//	badge     text, tone
//	code      code, lang
//	diff      diff
//	list      items, ordered
//	table     columns, rows
//	keyValue  rows（每行是 [键, 值]）
//	progress  value（数字 0–1）, label
//	button    label, action, variant
//	link      label, action
//	form      schema, value（初始值，对象）, submit, action
//	divider   （无字段）
//
// tone 的词表按节点不同，不要混用：
//
//	text   default / muted / danger / success
//	badge  neutral / accent / success / danger / warning
var DeclNodeTypes = []string{
	"stack", "row",
	"text", "markdown", "badge", "code", "diff",
	"list", "table", "keyValue",
	"progress",
	"button", "link",
	"form",
	"divider",
}

// DeclActionKinds 合法的动作种类。
var DeclActionKinds = []string{"call", "openPanel", "insert", "respond"}
