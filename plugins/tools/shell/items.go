package shell

// 权限项声明（06 - 工具权限设计方案 §9.3）。
//
// shell.exec：match 型，Bounded = false —— 命令文本是另一门语言的程序，匹配只减少询问次数，
// 不构成边界（界面须如实标注）。真正的边界是沙箱；沙箱关闭时这里没有边界。
// shell.job：结束 / 读取本进程起的后台 job，不引入新行为，各档位都放行。

import (
	"encoding/json"

	"ybase-kernel/core/toolhub"
)

// 权限项 id。
const (
	ItemExec = "shell.exec"
	ItemJob  = "shell.job"
)

// Items 本插件声明的权限项。
func Items() []toolhub.Item {
	return []toolhub.Item{
		{
			ID:      ItemExec,
			Title:   "执行终端命令",
			Type:    toolhub.ItemMatch,
			Class:   toolhub.EffectExec,
			Bounded: false,
			Subject: execSubjects,
			// 停止线：启动器类命令的前缀规则等于放行任意程序，“本次会话内都允许”不为它们生成规则。
			NoSessionAllow: Launcher,
			Suggest: map[string]toolhub.ItemAction{
				"read-only": toolhub.ActionAsk,
				"ask":       toolhub.ActionAsk,
				"workspace": toolhub.ActionAllow,
				"full":      toolhub.ActionAllow,
			},
		},
		{
			ID:    ItemJob,
			Title: "管理后台任务",
			Type:  toolhub.ItemSwitch,
			// 只读取 / 终止本进程已起的 job（起 job 本身已经过 shell.exec 判定），不引入新行为，
			// 按读类封顶；若归 exec 类，只读与询问档会被封顶为 ask，读输出也要逐次确认。
			Class: toolhub.EffectRead,
			Suggest: map[string]toolhub.ItemAction{
				"read-only": toolhub.ActionAllow,
				"ask":       toolhub.ActionAllow,
				"workspace": toolhub.ActionAllow,
				"full":      toolhub.ActionAllow,
			},
		},
	}
}

// execSubjects 把命令拆成简单命令，去掉无副作用的段，作为规则匹配对象。
// 拆不开（命令替换、脚本块、重定向）时返回整条命令：只有写整条命令的规则才能命中它。
func execSubjects(_ string, args json.RawMessage) ([]string, error) {
	var a struct {
		Command string `json:"command"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, nil // 参数不合法，工具自己会报错
		}
	}
	if a.Command == "" {
		return nil, nil
	}
	segs, ok := SplitCommand(a.Command)
	if !ok {
		return []string{a.Command}, nil
	}
	var out []string
	for _, s := range segs {
		if !Safe(s) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		out = []string{segs[0]}
	}
	return out, nil
}
