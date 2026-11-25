package sessions

// 权限项声明（06 - 工具权限设计方案 §9.3；Mortise docs/76 第四版 §16.5 权限拆分）。
//
// session.start：用 session_start 新建会话。子会话用同一套档位，它自己的每次工具调用由子会话各自判定，
// 所以这一项管的是“能不能派”，不是“对方能做什么”。
// session.read：读本会话与本会话派出的会话（session_turns、session_read、session_list）。只读，四档全放行，Plan 模式保留。
// session.manage：往派出的会话投消息、停止它们（session_send、session_stop，C3b）。

import "github.com/Sevyn17/tenon/runtime/toolhub"

// 权限项 id。
const (
	ItemSpawn  = "session.start"
	ItemRead   = "session.read"
	ItemManage = "session.manage"
)

// Items 本插件声明的权限项。
func Items() []toolhub.Item {
	return []toolhub.Item{{
		ID:    ItemSpawn,
		Title: "新建会话（委派）",
		Type:  toolhub.ItemSwitch,
		Class: toolhub.EffectSpawn,
		Suggest: map[string]toolhub.ItemAction{
			"read-only": toolhub.ActionAllow,
			"ask":       toolhub.ActionAsk,
			"workspace": toolhub.ActionAllow,
			"full":      toolhub.ActionAllow,
		},
	}, {
		ID:    ItemRead,
		Title: "回顾会话（读本会话与派出的会话）",
		Type:  toolhub.ItemSwitch,
		Class: toolhub.EffectRead,
		Suggest: map[string]toolhub.ItemAction{
			"read-only": toolhub.ActionAllow,
			"ask":       toolhub.ActionAllow,
			"workspace": toolhub.ActionAllow,
			"full":      toolhub.ActionAllow,
		},
	}, {
		ID:    ItemManage,
		Title: "给派出的会话发消息、停止它们",
		Type:  toolhub.ItemSwitch,
		Class: toolhub.EffectCollab,
		Suggest: map[string]toolhub.ItemAction{
			"read-only": toolhub.ActionDeny,
			"ask":       toolhub.ActionAsk,
			"workspace": toolhub.ActionAllow,
			"full":      toolhub.ActionAllow,
		},
	}}
}
