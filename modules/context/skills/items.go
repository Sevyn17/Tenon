package skills

// 权限项声明（06 - 工具权限设计方案 §9.3）。
//
// skills.load：按名字装载技能说明。只读本机技能目录里的文档，不改动任何东西，
// 所以四个内置档位都放行；仍然登记成权限项（而不是声明 NoAuth），这样自定义档位
// 可以把它关掉。

import "ybase/runtime/toolhub"

// ItemLoad 权限项 id。
const ItemLoad = "skills.load"

// Items 本插件声明的权限项。
func Items() []toolhub.Item {
	return []toolhub.Item{{
		ID:    ItemLoad,
		Title: "装载技能说明",
		Type:  toolhub.ItemSwitch,
		Class: toolhub.EffectRead,
		Suggest: map[string]toolhub.ItemAction{
			"read-only": toolhub.ActionAllow,
			"ask":       toolhub.ActionAllow,
			"workspace": toolhub.ActionAllow,
			"full":      toolhub.ActionAllow,
		},
	}}
}
