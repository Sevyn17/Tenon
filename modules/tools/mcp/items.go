package mcp

// 权限项声明（06 - 工具权限设计方案 §9.3）。
//
// MCP 的工具是启动时从服务端拉回来的，数量和名字都由服务端决定，所以权限项按“服务”一个，
// 类型是匹配型，匹配对象就是模型侧工具名 mcp__<服务>__<工具>（§9.12：规则里 server 段不许带
// 通配，否则会把以后新装的服务一并放行）。
//
// 类别随服务的 trust 字段：readonly → read，trusted → write，缺省 ask → external。
// 服务端自己声明的 destructiveHint 只能让判定更严（allow 降 ask），不能放宽，见 §9.5。

import (
	"encoding/json"
	"strings"

	"tenon/runtime/toolhub"
)

// ItemID 某个 MCP 服务的权限项 id。
func ItemID(server string) string { return "mcp." + server }

// Items 本插件声明的权限项：每个配置里的服务一项。
func Items(cfg Config) []toolhub.Item {
	out := make([]toolhub.Item, 0, len(cfg.Server))
	for _, sv := range cfg.Server {
		class, suggest := toolhub.EffectExternal, map[string]toolhub.ItemAction{
			"read-only": toolhub.ActionAsk,
			"ask":       toolhub.ActionAsk,
			"workspace": toolhub.ActionAsk,
			"full":      toolhub.ActionAllow,
		}
		switch sv.Trust {
		case TrustReadonly:
			class, suggest = toolhub.EffectRead, map[string]toolhub.ItemAction{
				"read-only": toolhub.ActionAllow,
				"ask":       toolhub.ActionAllow,
				"workspace": toolhub.ActionAllow,
				"full":      toolhub.ActionAllow,
			}
		case TrustTrusted:
			class, suggest = toolhub.EffectWrite, map[string]toolhub.ItemAction{
				"read-only": toolhub.ActionDeny,
				"ask":       toolhub.ActionAsk,
				"workspace": toolhub.ActionAllow,
				"full":      toolhub.ActionAllow,
			}
		}
		out = append(out, toolhub.Item{
			ID:      ItemID(sv.Name),
			Title:   "MCP 服务 " + sv.Name,
			Type:    toolhub.ItemMatch,
			Class:   class,
			Bounded: true,
			Subject: func(tool string, _ json.RawMessage) ([]string, error) {
				if tool == "" {
					return nil, nil
				}
				return []string{strings.ToLower(tool)}, nil
			},
			Suggest: suggest,
		})
	}
	return out
}
