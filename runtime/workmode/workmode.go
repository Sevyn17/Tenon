// Package workmode 会话的工作方式（客户端改进 M12，docs/50 第 5.2 节 Plan 模式）。
//
// 工作方式是会话状态，存在会话头 Ext 的 work_mode 键里：没有这个键 = execute（缺省），"plan" = Plan 模式。
// 它不是权限档位：权限档位管「允许做什么」，工作方式管「现在该做什么」，Plan 模式不改用户的档位。
// Plan 模式作用在四个环节：
//  1. 上下文组装：agent 追加一条状态更新（Instruction），要求先调查、给出计划、不做改动；
//  2. 工具显露：agent 构造请求时按 Allowed 去掉会写文件、执行命令的工具，模型看不到它们；
//  3. 执行前把关：assemble 登记的准入判定拒绝 Allowed 为假的调用，permission 再叠一层 read-only 上限；
//  4. 回合结束：plan-mode 插件的 exit_plan 工具提交计划，等用户批准。
package workmode

import "github.com/Sevyn17/tenon/runtime/toolhub"

const (
	// ExtKey 会话头 Ext 里的键。
	ExtKey = "work_mode"
	// Execute 缺省：正常执行。
	Execute = "execute"
	// Plan 只调查、给计划，不改动。
	Plan = "plan"
	// ExitPlanTool 提交计划的工具名（plan-mode 插件）。只在 Plan 模式下显露。
	ExitPlanTool = "exit_plan"
	// StatusKey Plan 模式指令在状态更新里的键。
	StatusKey = "work_mode"
	// InteractionKind 计划卡的交互种类。
	InteractionKind = "plan"
	// 计划卡的选择（回答 value.choice）。
	ChoiceApprove  = "approve"
	ChoiceRevise   = "revise"
	ChoiceContinue = "continue"
)

// Valid 是否认识的取值。
func Valid(s string) bool { return s == Execute || s == Plan }

// Of 会话头 Ext 里的工作方式；没有或不认识按 execute。
func Of(ext map[string]any) string {
	if s, _ := ext[ExtKey].(string); s == Plan {
		return Plan
	}
	return Execute
}

// ExtValue SetExt 要写的值：execute 写 nil（删键，和没设过的会话一样）。
func ExtValue(mode string) any {
	if mode == Plan {
		return Plan
	}
	return nil
}

// Instruction Plan 模式下追加给模型的指令。
const Instruction = "当前处于 Plan 模式：先调查，再给出计划，不要做任何改动。\n" +
	"- 只用读取、搜索、查询类工具了解情况；写文件、执行命令的工具在这个模式下不可用。\n" +
	"- 想清楚之后调用 exit_plan 提交计划（plan 写完整的计划正文，steps 列出可以逐项执行的步骤），等用户批准。\n" +
	"- 用户批准后会退出 Plan 模式并开一个新回合按计划执行；用户要求修改时按意见调整后再次提交。"

// Allowed Plan 模式下工具是否可用（显露给模型、允许执行）。按工具已有的分类判断：
//   - exit_plan、声明无需授权的工具（ask_user、todo_write 这类只和用户交互的）可用；
//   - MCP 工具声明了 readOnlyHint（AttrReadOnly）的可用；
//   - 可能改工作区文件的（AttrMayWrite、AttrWritePath）不可用；
//   - 没有声明权限项的工具当作会写，不可用；
//   - 权限项的效果类别全是 read / network / interact 的可用，有别的类别（write、exec、spawn、collab、
//     orchestrate、external）就不可用。
func Allowed(t *toolhub.Tool, items []toolhub.Item) bool {
	if t == nil {
		return false
	}
	if t.Spec.Name == ExitPlanTool || t.NoAuth {
		return true
	}
	if ro, _ := t.Attrs[toolhub.AttrReadOnly].(bool); ro {
		return true
	}
	if may, _ := t.Attrs[toolhub.AttrMayWrite].(bool); may {
		return false
	}
	if p, _ := t.Attrs[toolhub.AttrWritePath].(string); p != "" {
		return false
	}
	if len(items) == 0 {
		return false
	}
	for _, it := range items {
		switch it.Class {
		case toolhub.EffectRead, toolhub.EffectNetwork, toolhub.EffectInteract:
		default:
			return false
		}
	}
	return true
}
