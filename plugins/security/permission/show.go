package permission

// 可视化出口（06 - 工具权限设计方案 §9.6、§9.10）：mode show 打印解析后的完整取值表并标注来源；
// 自定义档位的名字不许单独出现，处处带放宽清单。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"ybase-kernel/core/toolhub"
)

// SetItems 记下装配汇总的权限项注册表（第一次构造会话时由权限层登记）。
func (s *Sessions) SetItems(items []toolhub.Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = append([]toolhub.Item(nil), items...)
	}
}

// SetLookup 记下“工具名 → 权限项”的查询（第一次构造会话时登记；各会话工具集相同）。
func (s *Sessions) SetLookup(f func(tool string) []toolhub.Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lookup == nil {
		s.lookup = f
	}
}

// SessionAllow “本次会话内都允许”要生成的规则（§9.8 + 两层控制停止线）：
// 去掉被权限项 NoSessionAllow 拦下的段（如启动器类命令）；blocked 是被拦下的段，
// 全部被拦下时 rules 为空，调用方应只批准本次。
func (s *Sessions) SessionAllow(tool string, args json.RawMessage) (rules []Rule, blocked []string) {
	s.mu.Lock()
	lk := s.lookup
	s.mu.Unlock()
	var stop []func(string) bool
	if lk != nil {
		for _, it := range lk(tool) {
			if it.NoSessionAllow != nil {
				stop = append(stop, it.NoSessionAllow)
			}
		}
	}
	for _, r := range SessionAllow(tool, args) {
		seg := strings.TrimSuffix(r.Pattern, ":*")
		bad := false
		for _, f := range stop {
			if seg != "" && f(seg) {
				bad = true
				break
			}
		}
		if bad {
			blocked = append(blocked, seg)
			continue
		}
		rules = append(rules, r)
	}
	return rules, blocked
}

// Items 权限项注册表（还没有会话构造过时为空）。
func (s *Sessions) Items() []toolhub.Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]toolhub.Item(nil), s.items...)
}

// Widened 自定义档位相对其基础内置档位放宽的权限项 id（内置档位返回空）。
// 放宽 = 缺省取值比基础档位建议值宽，或加了 allow 模式。
func Widened(d *ModeDef, items []toolhub.Item) []string {
	if d == nil || d.Builtin {
		return nil
	}
	var out []string
	for _, it := range items {
		s, ok := d.ItemOf(it.ID)
		if !ok || it.Type == toolhub.ItemLevel {
			continue
		}
		base := suggest(it, d.Root)
		cur := s.Default
		if cur == "" {
			cur = base
		}
		if cur.Severity() < base.Severity() || len(s.Allow) > 0 {
			out = append(out, it.ID)
		}
	}
	sort.Strings(out)
	return out
}

// Label 档位的显示名：自定义档位带基础档位与放宽清单，如
// “local（基于 workspace，放宽 2 项：shell.exec、web.fetch）”。
func Label(d *ModeDef, items []toolhub.Item) string {
	if d == nil {
		return ""
	}
	name := string(d.ID)
	if d.Title != "" && d.Title != name {
		name += " " + d.Title
	}
	if d.Builtin {
		return name
	}
	base := string(d.Root)
	if base == "" {
		base = "无内置基础档位"
	} else {
		base = "基于 " + base
	}
	w := Widened(d, items)
	if len(w) == 0 {
		return fmt.Sprintf("%s（%s）", name, base)
	}
	return fmt.Sprintf("%s（%s，放宽 %d 项：%s）", name, base, len(w), strings.Join(w, "、"))
}

// Show 档位解析后的完整取值表：每个权限项一行，含取值、模式清单与来源。
func Show(d *ModeDef, items []toolhub.Item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "档位 %s\n", Label(d, items))
	if d.Description != "" {
		fmt.Fprintf(&b, "  %s\n", d.Description)
	}
	fmt.Fprintf(&b, "  来源：%s\n", d.Source)
	if len(items) == 0 {
		b.WriteString("  （权限项注册表为空：还没有装配会话）\n")
		return b.String()
	}
	for _, it := range items {
		s, src := ItemValue(d, it)
		val := string(s.Default)
		if it.Type == toolhub.ItemLevel {
			val = s.Level
		}
		line := fmt.Sprintf("  %-18s %-6s %s", it.ID, val, it.Title)
		var extra []string
		for _, g := range []struct {
			k  string
			xs []string
		}{{"deny", s.Deny}, {"ask", s.Ask}, {"allow", s.Allow}} {
			if len(g.xs) > 0 {
				extra = append(extra, g.k+" "+strings.Join(g.xs, " "))
			}
		}
		if len(extra) > 0 {
			line += "；" + strings.Join(extra, "；")
		}
		if it.Type == toolhub.ItemMatch && !it.Bounded {
			line += "（匹配只按文本，不构成边界）"
		}
		fmt.Fprintf(&b, "%s  ← %s\n", line, src)
	}
	return b.String()
}
