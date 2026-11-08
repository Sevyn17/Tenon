package codemode

// 权限项声明（06 - 工具权限设计方案 §9.3）。
//
// codemode.run：run_code 自己不碰文件也不起进程，它只编排别的工具调用；每个子调用
// （调用 id `<父>#<n>`）都回到同一个 hub 重新判定，所以这一项管的是“能不能用编排”，
// 不需要对代码文本做匹配（开集变回闭集，见 §十）。
import "github.com/Sevyn17/tenon/runtime/toolhub"

// ItemRun 权限项 id。
const ItemRun = "codemode.run"

// Items 本插件声明的权限项。
func Items() []toolhub.Item {
	return []toolhub.Item{{
		ID:    ItemRun,
		Title: "用代码编排工具调用",
		Type:  toolhub.ItemSwitch,
		Class: toolhub.EffectOrchestrate,
		Suggest: map[string]toolhub.ItemAction{
			"read-only": toolhub.ActionAllow,
			"ask":       toolhub.ActionAsk,
			"workspace": toolhub.ActionAllow,
			"full":      toolhub.ActionAllow,
		},
	}}
}
