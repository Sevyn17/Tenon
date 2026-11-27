package agent

// Mortise docs/63 F2 端到端：名字不在冻结清单里的调用照常发 Need::Tools，由执行侧回填。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// callsModel：首轮调给定工具，看到工具结果后回一句文本结束。
type callsModel struct{ names []string }

func (m callsModel) Stream(_ context.Context, req session.Request, _ func(string)) (session.AssistantItem, session.StopReason, error) {
	if req.Messages[len(req.Messages)-1].Role == "tool" {
		return session.AssistantItem{Content: session.Content{session.NewTextPart("好")}}, session.StopReason{V: "EndTurn"}, nil
	}
	var cs []session.ToolCall
	for i, n := range m.names {
		cs = append(cs, session.ToolCall{WireID: "w" + string(rune('a'+i)), Name: n, Args: json.RawMessage(`{}`)})
	}
	return session.AssistantItem{ToolCalls: cs}, session.StopReason{V: "ToolUse"}, nil
}

func toolResults(a *Agent) []string {
	var out []string
	for _, m := range History(a.Core) {
		if m.Role == "tool" {
			out = append(out, contentText(m.Content))
		}
	}
	return out
}

func f2Agent(t *testing.T, names []string, extra ...*toolhub.Tool) *Agent {
	t.Helper()
	a := newAgent(t, tempStore(t), agentOpts{model: callsModel{names: names}})
	for _, x := range extra { // 会话创建后才登记：模拟档位放宽
		if err := a.Hub.Register(x); err != nil {
			t.Fatal(err)
		}
	}
	a.Toolset = a.Hub.SpecsFor(nil)
	return a
}

// 一步只调一个被放宽的工具：拿到"新会话生效"，且不执行。
func TestF2_LoosenedToolSingleCall(t *testing.T) {
	ran := 0
	bash := &toolhub.Tool{Spec: session.ToolSpec{Name: "bash", Schema: json.RawMessage(`{"type":"object"}`)},
		Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) { ran++; return toolhub.Text("x"), nil }}
	a := f2Agent(t, []string{"bash"}, bash)
	if r, err := a.Send(context.Background(), "跑"); err != nil || r.Kind != session.EndDone {
		t.Fatalf("%s %v", r, err)
	}
	rs := toolResults(a)
	if len(rs) != 1 || !strings.Contains(rs[0], "新会话生效") || ran != 0 {
		t.Fatalf("应提示新会话生效且不执行：%q ran=%d", rs, ran)
	}
}

// 一步全是真不存在的工具：结果文案与顺序与原 T13 立即回填一致。
func TestF2_AllMissingSameAsBefore(t *testing.T) {
	a := f2Agent(t, []string{"ghost1", "ghost2"})
	if r, err := a.Send(context.Background(), "跑"); err != nil || r.Kind != session.EndDone {
		t.Fatalf("%s %v", r, err)
	}
	rs := toolResults(a)
	want := []string{"工具 ghost1 不存在，可用工具：echo", "工具 ghost2 不存在，可用工具：echo"}
	if len(rs) != 2 || rs[0] != want[0] || rs[1] != want[1] {
		t.Fatalf("结果或顺序变了：%q", rs)
	}
}
