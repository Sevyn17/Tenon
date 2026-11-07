package permission

// 按权限项判定（06 - 工具权限设计方案 §9.5）。
//
// 每个工具声明它涉及的权限项（toolhub.Item），档位表给每项取值；本次调用涉及的所有项逐项求值，
// 任一 deny 即拒、任一 ask 即问、全部 allow 才放行。档位没写的项沿继承链回落到最近内置档位的
// 建议值（再回落封顶值），链上没有内置档位时回落“询问”，不回落 allow。

import (
	"encoding/json"
	"fmt"

	"tenon/runtime/toolhub"
)

// suggest 权限项在内置档位 root 下的建议值：插件建议值 → 类别封顶值 → 询问。
func suggest(it toolhub.Item, root Mode) toolhub.ItemAction {
	if root == "" {
		return toolhub.ActionAsk
	}
	if a, ok := it.Suggest[string(root)]; ok && a.Severity() >= 0 {
		return a
	}
	if a, ok := Cap(root, it.Class); ok {
		return a
	}
	return toolhub.ActionAsk
}

// suggestLevel level 型项在内置档位 root 下的建议级别：插件建议 → 第一个级别。
func suggestLevel(it toolhub.Item, root Mode) string {
	if lv, ok := it.SuggestLevel[string(root)]; ok {
		return lv
	}
	if len(it.Levels) > 0 {
		return it.Levels[0]
	}
	return ""
}

// ItemValue 权限项在档位 d 下的取值（含回落），以及取值来源说明（mode show 用）。
func ItemValue(d *ModeDef, it toolhub.Item) (ItemSetting, string) {
	s, ok := d.ItemOf(it.ID)
	src := "档位 " + d.Title
	if !ok || (s.Default == "" && it.Type != toolhub.ItemLevel) || (it.Type == toolhub.ItemLevel && s.Level == "") {
		fb := "建议值（" + it.Owner + "）"
		if d.Root == "" {
			fb = "无内置基础档位，回落询问"
		}
		if s.Default == "" {
			s.Default = suggest(it, d.Root)
		}
		if it.Type == toolhub.ItemLevel && s.Level == "" {
			s.Level = suggestLevel(it, d.Root)
		}
		if !ok {
			src = fb
		}
	}
	return s, src
}

func toAction(a toolhub.ItemAction) Action {
	switch a {
	case toolhub.ActionAllow:
		return Allow
	case toolhub.ActionDeny:
		return Deny
	}
	return AskUser
}

func severity(a Action) int {
	switch a {
	case Allow:
		return 0
	case Deny:
		return 2
	}
	return 1
}

// evalItem 单项求值；level 型项不参与 allow/ask/deny（ok=false）。
func evalItem(d *ModeDef, it toolhub.Item, tool string, args json.RawMessage) (Decision, bool) {
	if it.Type == toolhub.ItemLevel {
		return Decision{}, false
	}
	s, _ := ItemValue(d, it)
	def := toAction(s.Default)
	if it.Type == toolhub.ItemSwitch {
		return Decision{Action: def, Reason: fmt.Sprintf("档位 %s：%s → %s", d.Title, it.Title, def)}, true
	}
	subs, err := it.Subject(tool, args)
	if err != nil {
		return Decision{Action: AskUser, Reason: fmt.Sprintf("%s：无法取出匹配对象（%v）", it.Title, err)}, true
	}
	if len(subs) == 0 {
		return Decision{Action: def, Reason: fmt.Sprintf("档位 %s：%s → %s", d.Title, it.Title, def)}, true
	}
	worst := Decision{Action: Allow}
	for _, sub := range subs {
		act, why := def, fmt.Sprintf("档位 %s：%s %q 未命中，按缺省 %s", d.Title, it.Title, sub, def)
		if p, ok := matchAny(s.Deny, sub); ok {
			act, why = Deny, fmt.Sprintf("档位 %s：%s %q 命中 deny %s", d.Title, it.Title, sub, p)
		} else if p, ok := matchAny(s.Ask, sub); ok {
			act, why = AskUser, fmt.Sprintf("档位 %s：%s %q 命中 ask %s", d.Title, it.Title, sub, p)
		} else if p, ok := matchAny(s.Allow, sub); ok {
			act, why = Allow, fmt.Sprintf("档位 %s：%s %q 命中 allow %s", d.Title, it.Title, sub, p)
		}
		if severity(act) > severity(worst.Action) || worst.Reason == "" {
			worst = Decision{Action: act, Reason: why}
		}
	}
	return worst, true
}

func matchAny(pats []string, s string) (string, bool) {
	for _, p := range pats {
		if Glob(p, s) {
			return p, true
		}
	}
	return "", false
}

// evalItems 本次调用涉及的所有项求值：任一 deny 拒，任一 ask 问，全 allow 放行。
// ok=false 表示工具没有参与判定的项（调用方按“未声明一律询问”处理）。
func evalItems(d *ModeDef, items []toolhub.Item, tool string, args json.RawMessage) (Decision, bool) {
	var out Decision
	any := false
	for _, it := range items {
		dec, ok := evalItem(d, it, tool, args)
		if !ok {
			continue
		}
		if !any || severity(dec.Action) > severity(out.Action) {
			out = dec
		}
		any = true
	}
	return out, any
}

// Visible 工具在档位 d 下是否进模型工具列表：与参数无关地必然 deny 的工具不显露（§9.5）。
// 只看 switch 型取 deny、或 match 型缺省 deny 且没有 allow/ask 模式的项。
func Visible(d *ModeDef, t *toolhub.Tool, items []toolhub.Item) bool {
	if t == nil || t.NoAuth {
		return true
	}
	for _, it := range items {
		if it.Type == toolhub.ItemLevel {
			continue
		}
		s, _ := ItemValue(d, it)
		if s.Default != toolhub.ActionDeny {
			continue
		}
		if it.Type == toolhub.ItemSwitch || (len(s.Allow) == 0 && len(s.Ask) == 0) {
			return false
		}
	}
	return true
}
