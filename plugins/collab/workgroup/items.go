package workgroup

// 权限项声明（06 - 工具权限设计方案 §9.3）。
//
// team.spawn：派成员（独立会话，成员自己的工具调用由成员会话各自判定）。
// team.send：给组内另一位发消息。
// team.board：改看板（新建 / 领取 / 完成任务）。
// team.view：读看板，只读。

import "ybase-kernel/core/toolhub"

// 权限项 id。
const (
	ItemSpawn = "team.spawn"
	ItemSend  = "team.send"
	ItemBoard = "team.board"
	ItemView  = "team.view"
)

// Items 本插件声明的权限项。
func Items() []toolhub.Item {
	collab := map[string]toolhub.ItemAction{
		"read-only": toolhub.ActionDeny,
		"ask":       toolhub.ActionAsk,
		"workspace": toolhub.ActionAllow,
		"full":      toolhub.ActionAllow,
	}
	return []toolhub.Item{
		{
			ID: ItemSpawn, Title: "派工作组成员", Type: toolhub.ItemSwitch, Class: toolhub.EffectSpawn,
			Suggest: map[string]toolhub.ItemAction{
				"read-only": toolhub.ActionDeny,
				"ask":       toolhub.ActionAsk,
				"workspace": toolhub.ActionAllow,
				"full":      toolhub.ActionAllow,
			},
		},
		{ID: ItemSend, Title: "给工作组成员发消息", Type: toolhub.ItemSwitch, Class: toolhub.EffectCollab, Suggest: collab},
		{ID: ItemBoard, Title: "改工作组看板", Type: toolhub.ItemSwitch, Class: toolhub.EffectCollab, Suggest: collab},
		{
			ID: ItemView, Title: "看工作组看板", Type: toolhub.ItemSwitch, Class: toolhub.EffectRead,
			Suggest: map[string]toolhub.ItemAction{
				"read-only": toolhub.ActionAllow,
				"ask":       toolhub.ActionAllow,
				"workspace": toolhub.ActionAllow,
				"full":      toolhub.ActionAllow,
			},
		},
	}
}
