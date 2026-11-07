package agent

import (
	"context"
	"encoding/json"
	"testing"

	"tenon/runtime/modelhub"
	"tenon/runtime/session"
)

// sinkModel：第一步先流推理、一个工具调用的片段，再回这个调用；第二步（工具结果之后）流正文。
// retryOnce 为真时第一步先发一次 Reset（模拟调度器重试）。
type sinkModel struct{ retryOnce bool }

func (m *sinkModel) Stream(ctx context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	return m.StreamSink(ctx, "", req, modelhub.Sink{Text: onText})
}

func (m *sinkModel) StreamSink(_ context.Context, _ string, req session.Request, sk modelhub.Sink) (session.AssistantItem, session.StopReason, error) {
	last := req.Messages[len(req.Messages)-1]
	if last.Role == "tool" {
		sk.Text("好了")
		return session.AssistantItem{Content: session.Content{session.NewTextPart("好了")}}, session.StopReason{V: "EndTurn"}, nil
	}
	if m.retryOnce {
		m.retryOnce = false
		sk.Tool(modelhub.ToolDelta{Index: 0, Name: "echo"})
		sk.Reset()
	}
	sk.Reasoning("想一想")
	sk.Tool(modelhub.ToolDelta{Index: 3, Name: "echo"})
	sk.Tool(modelhub.ToolDelta{Index: 3, Args: `{"s":`})
	sk.Tool(modelhub.ToolDelta{Index: 3, Args: `"pong"}`})
	return session.AssistantItem{ToolCalls: []session.ToolCall{{WireID: "w1", Name: "echo", Args: json.RawMessage(`{"s":"pong"}`)}}},
		session.StopReason{V: "ToolUse"}, nil
}

// protocol 2 流式关联（06 10.5）：step 的 entry_id 就是落库的 assistant 条目 id；工具片段的 call_id
// 就是条目里调用的 id 与工具结果的 call_id；重试后 attempt 加一、调用 id 重新分配。
func TestStreamEventsCorrelate(t *testing.T) {
	db := tempStore(t)
	var evs []StreamEvent
	a := newAgent(t, db, agentOpts{model: &sinkModel{retryOnce: true}})
	a.Hooks.OnStream = func(e StreamEvent) { evs = append(evs, e) }
	if _, err := a.Send(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	es := a.Committed().Entries
	var asst []*session.Entry
	var resultCall string
	for _, e := range es {
		switch e.Item.Kind {
		case session.KindAssistant:
			asst = append(asst, e)
		case session.KindToolResult:
			resultCall = e.Item.ToolResult.CallID
		}
	}
	if len(asst) != 2 {
		t.Fatalf("应有两条 assistant 条目：%d", len(asst))
	}
	call := asst[0].Item.Assistant.ToolCalls[0].ID
	if resultCall != call {
		t.Fatalf("工具结果的 call_id 应等于调用 id：%s vs %s", resultCall, call)
	}
	steps := map[string]bool{}
	var lastTool StreamEvent
	attempt := 0
	for _, e := range evs {
		switch e.Kind {
		case StreamStep:
			steps[e.EntryID] = true
		case StreamReset:
			attempt = e.Attempt
		case StreamTool:
			lastTool = e
		}
	}
	if !steps[asst[0].ID] || !steps[asst[1].ID] {
		t.Fatalf("step 的 entry_id 应等于 assistant 条目 id：%v vs %s / %s", steps, asst[0].ID, asst[1].ID)
	}
	if attempt != 2 {
		t.Fatalf("重试后 attempt 应为 2：%d", attempt)
	}
	if lastTool.CallID != call || lastTool.Attempt != 2 {
		t.Fatalf("工具片段的 call_id 应等于条目里的调用 id：%+v vs %s", lastTool, call)
	}
	for _, e := range evs {
		if e.Kind == StreamTool && e.Attempt == 1 && e.CallID == call {
			t.Fatal("重试前的片段不应复用调用 id")
		}
	}
}
