// Package planmode 提供 exit_plan 工具（客户端改进 M12，docs/50 第 5.2 节 Plan 模式）：
// 模型在 Plan 模式下调查完后提交计划，弹出计划卡（kind = plan）等用户回答：
//   - approve：退出 Plan 模式（work_mode 回到 execute；权限档位从没改过，不用恢复），
//     排一条输入，当前回合结束后立即开新回合按计划执行；有 todo_write 时要求先把步骤记成任务；
//   - revise：修改意见作为工具结果交给模型，留在 Plan 模式；
//   - continue：留在 Plan 模式继续调查。
package planmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/toolhub"
	"github.com/Sevyn17/tenon/runtime/workmode"
)

// Timeout 等用户回答计划卡的上限（与确认挂起一致）。
const Timeout = 24 * time.Hour

// Kind 计划卡的交互种类。
const Kind = "plan"

// 计划卡的选择（与 wire.PlanApprove / PlanRevise / PlanContinue 同值）。
const (
	Approve  = "approve"
	Revise   = "revise"
	Continue = "continue"
)

// Source 批准后排进去的那条输入的来源标记（条目 meta.source）。
const Source = "plan"

// Answer 计划卡的回答（response.value）。
type Answer struct {
	Choice   string `json:"choice"`
	Feedback string `json:"feedback,omitempty"`
}

// Asker 弹出计划卡并等回答：返回回答的 JSON 值。
type Asker func(ctx context.Context, sessionID, title string, body map[string]any, schema json.RawMessage) (json.RawMessage, error)

// Session 工具要用的会话操作（宿主实现，测试可替换）。
type Session interface {
	// Mode 会话当前的工作方式。
	Mode(sessionID string) (string, error)
	// Approve 退出 Plan 模式并排一条输入（当前回合结束后开新回合）。
	Approve(sessionID, kickoff string) error
}

// answerSchema 回答须满足的 schema。
var answerSchema = json.RawMessage(`{"type":"object","properties":{"choice":{"type":"string","enum":["approve","revise","continue"]},"feedback":{"type":"string"}},"required":["choice"]}`)

// ExitPlan exit_plan 工具。ask 或 s 为 nil 时调用直接失败。
func ExitPlan(ask Asker, s Session) *toolhub.Tool {
	no := false
	t := toolhub.Simple(workmode.ExitPlanTool,
		"Plan 模式下提交计划，等用户批准。plan 写完整的计划正文（Markdown）；steps 列出批准后要逐项执行的步骤。"+
			"用户批准后退出 Plan 模式，新回合按计划执行；要求修改时会给出意见。",
		`{"type":"object","properties":{
"plan":{"type":"string","description":"完整的计划正文（Markdown）"},
"steps":{"type":"array","items":{"type":"string"},"description":"可逐项执行的步骤（批准后转成任务）"}},"required":["plan"]}`, nil)
	t.Call = func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
		var a struct {
			Plan  string   `json:"plan"`
			Steps []string `json:"steps"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return toolhub.Failf("参数不合法：%v", err), nil
		}
		a.Plan = strings.TrimSpace(a.Plan)
		if a.Plan == "" {
			return toolhub.Failf("plan 不能为空"), nil
		}
		steps := make([]string, 0, len(a.Steps))
		seen := map[string]bool{}
		for _, st := range a.Steps {
			if st = strings.TrimSpace(st); st != "" && !seen[st] {
				seen[st] = true
				steps = append(steps, st)
			}
		}
		if ask == nil || s == nil || cx == nil {
			return toolhub.Failf("当前界面不能显示计划卡"), nil
		}
		sid := cx.SessionID
		if m, err := s.Mode(sid); err != nil {
			return toolhub.Failf("%v", err), nil
		} else if m != workmode.Plan {
			return toolhub.Failf("当前不在 Plan 模式，不需要提交计划，直接执行即可"), nil
		}
		body := map[string]any{"plan": a.Plan}
		if len(steps) > 0 {
			body["steps"] = steps
		}
		v, err := ask(ctx, sid, "计划", body, answerSchema)
		if err != nil {
			return toolhub.Failf("%v", err), nil
		}
		ans, err := parseAnswer(v)
		if err != nil {
			return toolhub.Failf("%v", err), nil
		}
		switch ans.Choice {
		case Approve:
			todo := false
			if cx.Hub != nil {
				_, todo = cx.Hub.Get("todo_write")
			}
			if err := s.Approve(sid, Kickoff(a.Plan, steps, todo)); err != nil {
				return toolhub.Failf("退出 Plan 模式失败：%v", err), nil
			}
			return toolhub.Text("用户已批准计划，已退出 Plan 模式。现在结束本回合，不要再调用工具；新回合会按计划执行。"), nil
		case Revise:
			fb := strings.TrimSpace(ans.Feedback)
			if fb == "" {
				fb = "（没有写具体意见）"
			}
			return toolhub.Text("用户要求修改计划，仍在 Plan 模式。修改意见：\n" + fb + "\n按意见调整后再次调用 exit_plan 提交。"), nil
		default:
			return toolhub.Text("用户选择继续计划，仍在 Plan 模式。继续调查，想清楚后再调用 exit_plan 提交。"), nil
		}
	}
	t.Parallel = &no
	t.Timeout = Timeout
	t.NoAuth = true
	return t
}

// parseAnswer 回答：{"choice","feedback"}；也收单个字符串（choice）。
func parseAnswer(v json.RawMessage) (Answer, error) {
	var a Answer
	if err := json.Unmarshal(v, &a); err != nil {
		var s string
		if json.Unmarshal(v, &s) != nil {
			return Answer{}, fmt.Errorf("计划卡的回答不合法：%s", v)
		}
		a.Choice = s
	}
	switch a.Choice {
	case Approve, Revise, Continue:
		return a, nil
	}
	return Answer{}, fmt.Errorf("计划卡的回答 %q 不认识", a.Choice)
}

// Kickoff 批准后新回合的输入：计划正文与步骤；todo 为真时要求先用 todo_write 把步骤记成任务。
func Kickoff(plan string, steps []string, todo bool) string {
	var b strings.Builder
	b.WriteString("计划已批准，现在按计划执行。\n\n<plan>\n")
	b.WriteString(plan)
	b.WriteString("\n</plan>\n")
	if len(steps) > 0 {
		b.WriteString("\n步骤：\n")
		for i, st := range steps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, st)
		}
		if todo {
			b.WriteString("\n先调用 todo_write 把上面的步骤逐条记成任务（content 用步骤原文，status 为 pending），再逐项执行并随进度更新。\n")
		}
	}
	return b.String()
}

// HostSession 按会话宿主实现 Session。
type HostSession struct{ H *host.Host }

// Mode 读最近一次提交的会话头。
func (s HostSession) Mode(id string) (string, error) {
	hd, ok := s.H.Get(id)
	if !ok {
		return "", errors.New("会话没有在本进程打开")
	}
	return workmode.Of(hd.Agent().Committed().Header.Ext), nil
}

// Approve 工具在回合内执行，actor 在等工具结果，所以只投递不等受理：
// SetExt 在下一个需求边界生效（下一步请求就不再是 Plan 模式），排队的输入在本回合结束后单独开回合。
// 存储不支持排队时退回普通输入（下一步插入当前回合，照样按计划执行）。
func (s HostSession) Approve(id, kickoff string) error {
	hd, ok := s.H.Get(id)
	if !ok {
		return errors.New("会话没有在本进程打开")
	}
	if err := hd.Post(host.Command{Kind: host.SetExt, Key: workmode.ExtKey, Value: workmode.ExtValue(workmode.Execute)}); err != nil {
		return err
	}
	return hd.Post(host.Command{Kind: host.Send, ItemID: host.NewItemID(), Text: kickoff, Source: Source, Queue: s.H.QueueSupported()})
}
