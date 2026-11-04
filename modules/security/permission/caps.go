package permission

// 封顶表（06 - 工具权限设计方案 §9.2）。
//
// 内置档位的语义上限：四个内置档位 × 九个类别，每格是该档位允许的最宽取值。
// 插件声明的建议值只能等于或严于本格，否则装配期拒绝启动 —— 插件自报只能加严不能放宽，
// 这样「档位名能预测上限」才成立（仅可查看就一定不会写盘，不管装了什么插件）。
//
// 这张表只约束**内置档位的生成**。自定义档位是用户自己写的，用户是权威，可以比基础档位更宽
// （§9.6），封顶表不管它；真正不可逾越的是公共 deny 规则与沙箱越界申请的逐次确认。

import (
	"fmt"
	"sort"

	"ybase/runtime/toolhub"
)

// Caps 封顶表：档位 → 类别 → 最宽取值。
var Caps = map[Mode]map[string]toolhub.ItemAction{
	ReadOnly: {
		toolhub.EffectRead:        toolhub.ActionAllow,
		toolhub.EffectWrite:       toolhub.ActionDeny,
		toolhub.EffectExec:        toolhub.ActionAsk,
		toolhub.EffectNetwork:     toolhub.ActionAsk,
		toolhub.EffectInteract:    toolhub.ActionAllow,
		toolhub.EffectSpawn:       toolhub.ActionAllow,
		toolhub.EffectCollab:      toolhub.ActionDeny,
		toolhub.EffectOrchestrate: toolhub.ActionAllow,
		toolhub.EffectExternal:    toolhub.ActionAsk,
	},
	Ask: {
		toolhub.EffectRead:        toolhub.ActionAllow,
		toolhub.EffectWrite:       toolhub.ActionAsk,
		toolhub.EffectExec:        toolhub.ActionAsk,
		toolhub.EffectNetwork:     toolhub.ActionAsk,
		toolhub.EffectInteract:    toolhub.ActionAllow,
		toolhub.EffectSpawn:       toolhub.ActionAsk,
		toolhub.EffectCollab:      toolhub.ActionAsk,
		toolhub.EffectOrchestrate: toolhub.ActionAsk,
		toolhub.EffectExternal:    toolhub.ActionAsk,
	},
	Workspace: {
		toolhub.EffectRead:        toolhub.ActionAllow,
		toolhub.EffectWrite:       toolhub.ActionAllow,
		toolhub.EffectExec:        toolhub.ActionAllow,
		toolhub.EffectNetwork:     toolhub.ActionAsk,
		toolhub.EffectInteract:    toolhub.ActionAllow,
		toolhub.EffectSpawn:       toolhub.ActionAllow,
		toolhub.EffectCollab:      toolhub.ActionAllow,
		toolhub.EffectOrchestrate: toolhub.ActionAllow,
		toolhub.EffectExternal:    toolhub.ActionAsk,
	},
	Full: {
		toolhub.EffectRead:        toolhub.ActionAllow,
		toolhub.EffectWrite:       toolhub.ActionAllow,
		toolhub.EffectExec:        toolhub.ActionAllow,
		toolhub.EffectNetwork:     toolhub.ActionAllow,
		toolhub.EffectInteract:    toolhub.ActionAllow,
		toolhub.EffectSpawn:       toolhub.ActionAllow,
		toolhub.EffectCollab:      toolhub.ActionAllow,
		toolhub.EffectOrchestrate: toolhub.ActionAllow,
		toolhub.EffectExternal:    toolhub.ActionAllow,
	},
}

// Cap 某内置档位某类别的封顶值；档位不是内置档位时返回 ask（自定义档位不查表）。
func Cap(m Mode, class string) (toolhub.ItemAction, bool) {
	row, ok := Caps[m]
	if !ok {
		return "", false
	}
	a, ok := row[class]
	return a, ok
}

// stricter 两个取值里更严的那个。
func stricter(a, b toolhub.ItemAction) toolhub.ItemAction {
	if b.Severity() > a.Severity() {
		return b
	}
	return a
}

// CheckItems 装配期校验注册表：建议值不得越过封顶，且 level 型不参与封顶（它不是开关）。
func CheckItems(items []toolhub.Item) error {
	var bad []string
	for _, it := range items {
		if it.Type == toolhub.ItemLevel {
			continue
		}
		for _, m := range Builtins {
			sug, ok := it.Suggest[string(m)]
			if !ok {
				continue
			}
			if _, err := toolhub.ParseAction(string(sug)); err != nil {
				return fmt.Errorf("permission: 权限项 %s（%s）在档位 %s 的建议值不合法：%w", it.ID, it.Owner, m, err)
			}
			top, has := Cap(m, it.Class)
			if !has {
				return fmt.Errorf("permission: 权限项 %s（%s）的类别 %s 不在封顶表里", it.ID, it.Owner, it.Class)
			}
			if sug.Severity() < top.Severity() {
				bad = append(bad, fmt.Sprintf("%s（%s）在档位 %s 建议 %s，封顶 %s", it.ID, it.Owner, m, sug, top))
			}
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("permission: 权限项建议值越过封顶表，拒绝启动：\n  %s", joinLines(bad))
	}
	return nil
}

func joinLines(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += "\n  "
		}
		out += x
	}
	return out
}
