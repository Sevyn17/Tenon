package subagent

// 权限项声明（06 - 工具权限设计方案 §9.3）。
//
// subagent.spawn：派子智能体。子会话用同一套档位，它自己的每次工具调用由子会话各自判定，
// 所以这一项管的是“能不能派”，不是“子智能体能做什么”。

import "ybase/runtime/toolhub"

// ItemSpawn 权限项 id。
const ItemSpawn = "subagent.spawn"

// Items 本插件声明的权限项。
func Items() []toolhub.Item {
	return []toolhub.Item{{
		ID:    ItemSpawn,
		Title: "委派子智能体",
		Type:  toolhub.ItemSwitch,
		Class: toolhub.EffectSpawn,
		Suggest: map[string]toolhub.ItemAction{
			"read-only": toolhub.ActionAllow,
			"ask":       toolhub.ActionAsk,
			"workspace": toolhub.ActionAllow,
			"full":      toolhub.ActionAllow,
		},
	}}
}
