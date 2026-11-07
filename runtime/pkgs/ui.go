package pkgs

// 插件包的界面贡献（[[ui]] 段，计划/05 F10）：往前端插槽放声明式视图（web/src/ui/declarative.tsx 的 DeclNode），
// 不写前端代码。视图可以写在清单里（view，TOML 内联表）或单独的 JSON 文件（view_file，相对包目录）。
// data 可选：一条只读协议命令，结果用来替换视图里的 {{路径}} 占位（{{session}} 是插槽所在的会话）。
// 本包只校验形状与大小；视图里认不认识的节点由前端判断（不认识的显示一行说明，不会让界面出错）。
//
//	[[ui]]
//	id = "deploy-status"            # 包内唯一
//	slot = "conversation.card"      # 插槽位置（见 web/README 的插槽清单）
//	title = "部署状态"
//	order = 50
//	requires = "mcp"                # 可选：这个后端插件开着才显示
//	view_file = "ui/status.json"
//	[ui.data]
//	cmd = "session.stats"
//	args = { session = "{{session}}" }

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// UISlot 一条界面贡献。View 是 JSON（前端的 DeclNode 或其数组）。
type UISlot struct {
	ID       string          `json:"id"`
	Slot     string          `json:"slot"`
	Title    string          `json:"title,omitempty"`
	Order    int             `json:"order,omitempty"`
	Requires string          `json:"requires,omitempty"`
	View     json.RawMessage `json:"view"`
	Data     *UIData         `json:"data,omitempty"`
}

// UIData 视图的数据来源：一条只读协议命令。
type UIData struct {
	Cmd  string         `json:"cmd"`
	Args map[string]any `json:"args,omitempty"`
}

// 视图上限：单条 64 KiB、一个包最多 32 条。
const (
	MaxUIView  = 64 << 10
	MaxUISlots = 32
)

// UIReadCommands 视图的数据来源只能是这些只读命令（不让声明式视图在渲染时改状态）。
var UIReadCommands = map[string]bool{
	"session.get": true, "session.stats": true, "session.entries": true, "sessions.list": true,
	"interactions.list": true, "plugins.list": true, "packages.list": true, "mode.get": true, "mode.list": true,
	"workspace.files": true, "models.list": true, "feedback.list": true,
}

var (
	uiIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	uiSlotRe = regexp.MustCompile(`^[a-z][a-z0-9.]{0,62}$`)
)

type uiItem struct {
	ID       string         `toml:"id"`
	Slot     string         `toml:"slot"`
	Title    string         `toml:"title"`
	Order    int            `toml:"order"`
	Requires string         `toml:"requires"`
	View     map[string]any `toml:"view"`
	ViewFile string         `toml:"view_file"`
	Data     *struct {
		Cmd  string         `toml:"cmd"`
		Args map[string]any `toml:"args"`
	} `toml:"data"`
}

func init() {
	RegisterSection(Section{Key: "ui", Label: "界面",
		Decode: func(dir string, md toml.MetaData, prim toml.Primitive) (any, error) {
			var xs []uiItem
			if err := md.PrimitiveDecode(prim, &xs); err != nil {
				return nil, fmt.Errorf("[[ui]]：%w", err)
			}
			return decodeUI(dir, xs)
		},
		Summary: func(v any) string {
			us, _ := v.([]UISlot)
			ss := make([]string, 0, len(us))
			for _, u := range us {
				ss = append(ss, u.Slot)
			}
			return "界面 " + strings.Join(ss, "、")
		}})
}

func decodeUI(dir string, xs []uiItem) ([]UISlot, error) {
	if len(xs) > MaxUISlots {
		return nil, fmt.Errorf("[[ui]] 最多 %d 条，得到 %d", MaxUISlots, len(xs))
	}
	seen := map[string]bool{}
	out := make([]UISlot, 0, len(xs))
	for _, x := range xs {
		if !uiIDRe.MatchString(x.ID) {
			return nil, fmt.Errorf("[[ui]] id %q 不合法（小写字母、数字与连字符）", x.ID)
		}
		if seen[x.ID] {
			return nil, fmt.Errorf("[[ui]] id %q 重复", x.ID)
		}
		seen[x.ID] = true
		if !uiSlotRe.MatchString(x.Slot) {
			return nil, fmt.Errorf("[[ui]] %s 的 slot %q 不合法", x.ID, x.Slot)
		}
		var view []byte
		switch {
		case x.ViewFile != "" && x.View != nil:
			return nil, fmt.Errorf("[[ui]] %s：view 与 view_file 只能给一个", x.ID)
		case x.ViewFile != "":
			p, err := within(dir, x.ViewFile)
			if err != nil {
				return nil, fmt.Errorf("[[ui]] %s 的 view_file：%v", x.ID, err)
			}
			if view, err = os.ReadFile(p); err != nil {
				return nil, fmt.Errorf("[[ui]] %s 的 view_file：%v", x.ID, err)
			}
			if !json.Valid(view) {
				return nil, fmt.Errorf("[[ui]] %s 的 view_file 不是合法 JSON", x.ID)
			}
		case x.View != nil:
			var err error
			if view, err = json.Marshal(x.View); err != nil {
				return nil, fmt.Errorf("[[ui]] %s 的 view：%v", x.ID, err)
			}
		default:
			return nil, fmt.Errorf("[[ui]] %s 缺 view 或 view_file", x.ID)
		}
		if len(view) > MaxUIView {
			return nil, fmt.Errorf("[[ui]] %s 的视图超过 %d 字节", x.ID, MaxUIView)
		}
		if err := checkViewShape(view); err != nil {
			return nil, fmt.Errorf("[[ui]] %s 的视图：%v", x.ID, err)
		}
		u := UISlot{ID: x.ID, Slot: x.Slot, Title: x.Title, Order: x.Order, Requires: x.Requires, View: view}
		if x.Data != nil {
			if !UIReadCommands[x.Data.Cmd] {
				return nil, fmt.Errorf("[[ui]] %s 的 data.cmd %q 不是允许的只读命令", x.ID, x.Data.Cmd)
			}
			u.Data = &UIData{Cmd: x.Data.Cmd, Args: x.Data.Args}
		}
		out = append(out, u)
	}
	return out, nil
}

// checkViewShape 视图是带 type 的对象或这样的对象数组（深层节点由前端校验）。
func checkViewShape(b []byte) error {
	var one struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(b, &one) == nil && one.Type != "" {
		return nil
	}
	var many []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(b, &many) == nil && len(many) > 0 {
		for i, n := range many {
			if n.Type == "" {
				return fmt.Errorf("第 %d 个节点缺 type", i+1)
			}
		}
		return nil
	}
	return fmt.Errorf("须是带 type 的节点或节点数组")
}

// UISlots 包里的界面贡献（没有返回 nil）。
func (p *Package) UISlots() []UISlot {
	us, _ := SectionValue[[]UISlot](p, "ui")
	return us
}
