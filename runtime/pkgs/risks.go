package pkgs

// 插件包的风险项（客户端改进 M11 配置T22 / T24，spike-config 第六、七节）：安装确认框与插件包列表据此提示。
// 推断规则：
//   - 交给组件的段（[[server]]、[[hook]]、[[ui]]）由登记段的组件给出（Section.Risks）：
//     mcp：stdio 服务（command）→ exec，沙箱不是 read-only 时再加 write；http 服务（url）→ network。
//     hooks：每条钩子 → exec，沙箱不是 read-only（缺省 workspace-write）时再加 write。
//     ui：视图里的按钮 / 链接 / 表单能调协议命令（kind = call）→ commands。
//   - 本包自己解析的：[config.*] → config；提示词片段或技能目录 → prompt（会改模型看到的系统提示词）。
//   - 定义目录（agents）不单列风险：定义里的工具仍受档位与沙箱约束。
// 结果按 RiskOrder 排序、去重。

import (
	"encoding/json"
	"sort"
)

// 风险项。取值与 appapi/wire 的 Risk* 常量相同（appapi 有测试核对）。
const (
	RiskExec     = "exec"     // 会在本机执行命令（stdio MCP 服务、钩子）
	RiskNetwork  = "network"  // 会连外部地址（http MCP 服务）
	RiskWrite    = "write"    // 执行的命令不在只读沙箱里，可能改文件
	RiskConfig   = "config"   // 改内置插件的缺省配置
	RiskPrompt   = "prompt"   // 改系统提示词（提示词片段、技能）
	RiskCommands = "commands" // 界面按钮能调协议命令
)

// RiskOrder 风险项的展示顺序（大致按严重程度）。
var RiskOrder = []string{RiskExec, RiskNetwork, RiskWrite, RiskConfig, RiskPrompt, RiskCommands}

// SortRisks 去重并按 RiskOrder 排序（不认识的排在最后，按字母序）。结果不为 nil。
func SortRisks(rs []string) []string {
	rank := map[string]int{}
	for i, r := range RiskOrder {
		rank[r] = i
	}
	seen := map[string]bool{}
	out := []string{}
	for _, r := range rs {
		if r != "" && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, iok := rank[out[i]]
		rj, jok := rank[out[j]]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		return out[i] < out[j]
	})
	return out
}

// Risks 这个包带来的风险项（规则见文件头）。没有时为空数组。
func (p *Package) Risks() []string {
	var rs []string
	for _, k := range p.SectionKeys() {
		if sec, ok := SectionOf(k); ok && sec.Risks != nil {
			rs = append(rs, sec.Risks(p.Sections[k])...)
		}
	}
	if len(p.Config) > 0 {
		rs = append(rs, RiskConfig)
	}
	if len(p.Prompts) > 0 || p.SkillsDir != "" {
		rs = append(rs, RiskPrompt)
	}
	return SortRisks(rs)
}

// UICommands 包的界面视图里按钮 / 链接 / 表单能调的协议命令（动作 kind = call 的 cmd，去重排序）。
// 数据来源（[[ui]] data.cmd）不算：它只能是只读命令（UIReadCommands）。
func (p *Package) UICommands() []string {
	seen := map[string]bool{}
	for _, u := range p.UISlots() {
		var v any
		if json.Unmarshal(u.View, &v) == nil {
			collectCalls(v, seen)
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func collectCalls(v any, seen map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		if k, _ := x["kind"].(string); k == "call" {
			if c, _ := x["cmd"].(string); c != "" {
				seen[c] = true
			}
		}
		for _, c := range x {
			collectCalls(c, seen)
		}
	case []any:
		for _, c := range x {
			collectCalls(c, seen)
		}
	}
}
