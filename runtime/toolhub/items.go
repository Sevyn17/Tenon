package toolhub

// 权限项注册表（06 - 工具权限设计方案 §9.3）。
//
// 三层词汇：类别（Effect*，宿主写死，只用于给内置档位封顶）→ 权限项（插件注册工具时声明，
// 宿主汇总成注册表，是判定与配置的单位）→ 取值（档位表给每项赋值，由权限插件解析）。
//
// 工具管理器只负责注册表的结构与校验，不解释取值：档位、封顶表、判定顺序都在权限插件。

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ItemAction 权限项取值（宿主固定三种，插件只能选不能扩展）。
type ItemAction string

const (
	ActionAllow ItemAction = "allow"
	ActionAsk   ItemAction = "ask"
	ActionDeny  ItemAction = "deny"
)

// Severity 严格程度：allow 0 < ask 1 < deny 2；未知 -1。
func (a ItemAction) Severity() int {
	switch a {
	case ActionAllow:
		return 0
	case ActionAsk:
		return 1
	case ActionDeny:
		return 2
	}
	return -1
}

// ParseAction 校验取值名。
func ParseAction(s string) (ItemAction, error) {
	switch a := ItemAction(s); a {
	case ActionAllow, ActionAsk, ActionDeny:
		return a, nil
	}
	return "", fmt.Errorf("toolhub: 未知权限取值 %q（allow / ask / deny）", s)
}

// ItemType 权限项类型（宿主固定三种）。
type ItemType string

const (
	// ItemSwitch 开关：整项取 allow / ask / deny。
	ItemSwitch ItemType = "switch"
	// ItemMatch 匹配列表：deny / ask / allow 三个模式列表 + 未命中缺省；匹配对象由 Subject 从参数取出。
	ItemMatch ItemType = "match"
	// ItemLevel 级别：从 Levels 里取一个值（如沙箱级别）。
	ItemLevel ItemType = "level"
)

// Item 一个权限项。由插件在注册工具时声明，用户不可编辑。
type Item struct {
	ID    string // "shell.exec"、"web.fetch"、"sandbox.level"
	Title string // 面板显示名
	Type  ItemType
	Class string // 所属类别（Effect* 之一），权限插件据此查封顶表
	// Levels 仅 level 型：可取的级别，第一个为缺省。
	Levels []string
	// Bounded 仅 match 型：匹配是否构成边界。
	// true = 参数是数据（路径、域名），规范化后比对即是边界；
	// false = 参数是另一门语言的程序（shell 命令），匹配只减少询问次数，不构成边界，界面须如实标注。
	Bounded bool
	// Subject 仅 match 型：从工具名与调用参数取出匹配对象（可多个）。
	// 由声明该项的插件提供：shell 插件知道怎么把命令拆段、MCP 插件知道怎么从工具名取方法名，
	// 权限插件不需要知道。
	Subject func(tool string, args json.RawMessage) ([]string, error)
	// Suggest 各内置档位的建议值，键是内置档位 id。缺某档位时由权限插件按封顶值回落。
	Suggest map[string]ItemAction
	// SuggestLevel 仅 level 型：各内置档位的建议级别，键是内置档位 id，取值须在 Levels 内。
	SuggestLevel map[string]string
	// Owner 声明来源（插件名），`tenon mode show` 标注用；由注册方填写。
	Owner string
	// NoSessionAllow 仅 match 型、可空：哪些匹配对象永不能被“本次会话内都允许”生成的规则放行
	// （shell 的启动器类命令：sh / node / npm run …，放行前缀等于放行任意程序）。
	NoSessionAllow func(subject string) bool
}

// Validate 结构校验（不含封顶校验，那是权限插件的事）。
func (it Item) Validate() error {
	if it.ID == "" {
		return fmt.Errorf("toolhub: 权限项缺 ID")
	}
	if it.Title == "" {
		return fmt.Errorf("toolhub: 权限项 %s 缺 Title", it.ID)
	}
	if !validClass(it.Class) {
		return fmt.Errorf("toolhub: 权限项 %s 的类别 %q 不在九个类别内", it.ID, it.Class)
	}
	switch it.Type {
	case ItemSwitch:
		if it.Subject != nil || len(it.Levels) > 0 {
			return fmt.Errorf("toolhub: 权限项 %s 是 switch 型，不应带 Subject / Levels", it.ID)
		}
	case ItemMatch:
		if it.Subject == nil {
			return fmt.Errorf("toolhub: 权限项 %s 是 match 型，必须提供 Subject", it.ID)
		}
	case ItemLevel:
		if len(it.Levels) == 0 {
			return fmt.Errorf("toolhub: 权限项 %s 是 level 型，必须提供 Levels", it.ID)
		}
	default:
		return fmt.Errorf("toolhub: 权限项 %s 的类型 %q 非法（switch / match / level）", it.ID, it.Type)
	}
	for mode, a := range it.Suggest {
		if a.Severity() < 0 {
			return fmt.Errorf("toolhub: 权限项 %s 在档位 %s 的建议值 %q 非法", it.ID, mode, a)
		}
	}
	for mode, lv := range it.SuggestLevel {
		if it.Type != ItemLevel {
			return fmt.Errorf("toolhub: 权限项 %s 不是 level 型，不应带 SuggestLevel", it.ID)
		}
		if !hasLevel(it.Levels, lv) {
			return fmt.Errorf("toolhub: 权限项 %s 在档位 %s 的建议级别 %q 不在 Levels 内", it.ID, mode, lv)
		}
	}
	return nil
}

func hasLevel(levels []string, v string) bool {
	for _, l := range levels {
		if l == v {
			return true
		}
	}
	return false
}

func validClass(c string) bool {
	for _, e := range Effects {
		if e == c {
			return true
		}
	}
	return false
}

// RegisterItem 登记一个权限项（同 id 重复注册报错）。
func (h *Hub) RegisterItem(it Item) error {
	if err := it.Validate(); err != nil {
		return err
	}
	if old, dup := h.items[it.ID]; dup {
		return fmt.Errorf("toolhub: 权限项重名 %s（已由 %s 声明）", it.ID, old.Owner)
	}
	h.items[it.ID] = it
	return nil
}

// LookupItem 按 id 取权限项。
func (h *Hub) LookupItem(id string) (Item, bool) {
	it, ok := h.items[id]
	return it, ok
}

// Items 全部权限项，按 id 排序（面板与 mode show 用）。
func (h *Hub) Items() []Item {
	out := make([]Item, 0, len(h.items))
	for _, it := range h.items {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ItemsOf 本次调用涉及的权限项。工具未声明任何项时返回 nil：
// 判定方必须据此询问（未注册权限项的工具一律询问，漏声明的代价是啰嗦，不是失守）。
func (h *Hub) ItemsOf(t *Tool) []Item {
	if t == nil {
		return nil
	}
	var out []Item
	for _, id := range t.Items {
		if it, ok := h.items[id]; ok {
			out = append(out, it)
		}
	}
	return out
}

// SetVisible 由权限策略层在装配期注入可见性判定：取值 deny 的工具不进显露集，
// 模型看不到它（§9.5）。只允许设置一次，重复设置报错——可见性只能有一个来源。
func (h *Hub) SetVisible(f func(name string) bool) error {
	if h.visible != nil {
		return fmt.Errorf("toolhub: 工具可见性判定已被设置过")
	}
	h.visible = f
	return nil
}

// visibleTool 工具是否对模型可见（无判定时全部可见；内置工具不受档位影响）。
func (h *Hub) visibleTool(n string) bool {
	if h.visible == nil || builtinTool(n) {
		return true
	}
	return h.visible(n)
}

// Validate 装配期校验：工具引用的权限项必须已注册；必须恰好有一个负责授权的层。
// 由 runtime 在工具与层全部登记之后调用，不通过则拒绝启动。
func (h *Hub) Validate() error {
	for _, n := range h.order {
		t := h.tools[n]
		if builtinTool(n) {
			continue
		}
		for _, id := range t.Items {
			if _, ok := h.items[id]; !ok {
				return fmt.Errorf("toolhub: 工具 %s 引用了未注册的权限项 %s", n, id)
			}
		}
		if len(t.Items) == 0 && !t.NoAuth {
			// 不报错：未声明的工具按“一律询问”处理（见 ItemsOf）。
			continue
		}
		if t.NoAuth && len(t.Items) > 0 {
			return fmt.Errorf("toolhub: 工具 %s 既声明无需授权又引用了权限项", n)
		}
	}
	// 授权落点（04 4.3.1）：要么设了 Authorizer（决策服务），要么恰好一个 Authorizes 层
	// （迁移期兼容）。两者都没有 = 没人判定，失败即收紧——直接拒绝装配。
	n := 0
	for _, l := range h.layers {
		if l.Authorizes {
			n++
		}
	}
	switch {
	case h.authorizer != nil && n > 0:
		return fmt.Errorf("toolhub: 装配同时设了 Authorizer 与 %d 个权限策略层，只能二选一", n)
	case h.authorizer != nil:
		return nil
	case n != 1:
		return fmt.Errorf("toolhub: 装配必须恰好有一个权限策略层或一个 Authorizer，现在层 %d 个", n)
	}
	return nil
}
