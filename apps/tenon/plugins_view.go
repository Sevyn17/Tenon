package main

// 插件开关展示（plugin-switch-plan §3.4）：explain、启动横幅、/plugins 共用。

import (
	"fmt"
	"sort"
	"strings"

	"tenon/runtime/component"
	"tenon/runtime/pkgs"
)

// packageLines 已加载插件包的展示行：名字 版本（用户级 / 项目级）← 清单路径，下一行列内容。
func packageLines(ps []*pkgs.Package) []string {
	if len(ps) == 0 {
		return []string{"（无）"}
	}
	var out []string
	for _, k := range ps {
		scope := "用户级"
		if k.Scope == pkgs.ScopeProject {
			scope = "项目级"
		}
		ver := ""
		if k.Version != "" {
			ver = " " + k.Version
		}
		out = append(out, fmt.Sprintf("%s%s（%s）← %s", k.Name, ver, scope, k.File))
		desc := k.Contents()
		if k.Description != "" {
			desc = k.Description + "；" + desc
		}
		out = append(out, "    "+desc)
	}
	return out
}

var effectText = map[string]string{
	component.EffectSystemPrompt:  "改系统提示词",
	component.EffectTools:         "加工具",
	component.EffectRewriteResult: "改写结果",
	component.EffectExec:          "执行程序",
	component.EffectNetwork:       "联网",
}

func effects(es []string) string {
	var out []string
	for _, e := range es {
		if t, ok := effectText[e]; ok {
			out = append(out, t)
		} else {
			out = append(out, e)
		}
	}
	return strings.Join(out, "、")
}

// stateLine 一行：开/关 名字（影响）← 来源。
func stateLine(st component.State) string {
	on := "关"
	if st.On {
		on = "开"
	}
	s := on + " " + st.Name
	if len(st.Effects) > 0 {
		s += "（" + effects(st.Effects) + "）"
	}
	if st.Source != "" {
		s += " ← " + st.Source
	} else if st.Default {
		s += " ← 缺省开"
	} else {
		s += " ← 缺省关"
	}
	return s
}

// enabledLine 已开启插件名，非缺省开启的标 *。
func enabledLine(p *component.Profile) string {
	var on []string
	for _, st := range p.States() {
		if !st.On {
			continue
		}
		n := st.Name
		if !st.Default {
			n += "*"
		}
		on = append(on, n)
	}
	return strings.Join(on, " ") + "（* = 非缺省开启；tenon explain 看详情）"
}

// extAssembly 会话头 Ext 键：上次打开会话时开启的插件名（有序）。
const extAssembly = "assembly.plugins"

// onNames 已开启插件名（profile 顺序）。
func onNames(p *component.Profile) []string {
	var on []string
	for _, st := range p.States() {
		if st.On {
			on = append(on, st.Name)
		}
	}
	return on
}

// assemblyDiff 比较上次记录（Ext 反序列化后是 []any 或 []string）与这次，返回“新增 … ；关闭 …”，相同返回空。
func assemblyDiff(prev any, now []string) string {
	old := map[string]bool{}
	switch v := prev.(type) {
	case []string:
		for _, s := range v {
			old[s] = true
		}
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				old[s] = true
			}
		}
	default:
		return ""
	}
	cur := map[string]bool{}
	var added, removed []string
	for _, n := range now {
		cur[n] = true
		if !old[n] {
			added = append(added, n)
		}
	}
	for n := range old {
		if !cur[n] {
			removed = append(removed, n)
		}
	}
	sort.Strings(removed)
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "新增 "+strings.Join(added, " "))
	}
	if len(removed) > 0 {
		parts = append(parts, "关闭 "+strings.Join(removed, " "))
	}
	return strings.Join(parts, "；")
}
