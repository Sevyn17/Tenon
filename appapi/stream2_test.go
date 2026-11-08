package appapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/session"
)

// 流式与条目的关联（10.5）：turn.started 带吃进的 client_id；message.started 的 entry_id 等于随后 entry.appended 的
// entry.id；用户条目的 client_ids 记着输入；回合结束前发 message.ended，turn.ended 收尾。
func TestStreamCorrelatesWithEntries(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "s"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"你好","client_id":"c1"}`); e != nil {
		t.Fatal(e)
	}
	evs := waitKind(t, c, wire.EvTurnEnded)

	var (
		started  *wire.TurnStarted
		msg      *wire.MessageStarted
		ended    bool
		text     string
		userIDs  []string
		assistID string
	)
	for _, e := range evs {
		switch d := e.Data.(type) {
		case wire.TurnStarted:
			started = &d
		case wire.MessageStarted:
			msg = &d
		case wire.MessageDelta:
			text += d.Text
		case wire.StreamRef:
			if e.Kind == wire.EvMessageEnded && msg != nil && d.StreamID == msg.StreamID {
				ended = true
			}
		case wire.EntryAppended:
			switch d.Entry.Item.Kind {
			case wire.KindUser:
				userIDs = d.Entry.ClientIDs
			case wire.KindAssistant:
				assistID = d.Entry.ID
			}
		}
	}
	if started == nil || len(started.Inputs) != 1 || started.Inputs[0] != "c1" || started.Turn != 1 {
		t.Fatalf("turn.started 应带 inputs [c1]：%+v（%v）", started, kindsOf(evs))
	}
	if msg == nil || msg.Part != "text" || msg.Attempt != 1 {
		t.Fatalf("应有 message.started：%v", kindsOf(evs))
	}
	if msg.EntryID == "" || msg.EntryID != assistID {
		t.Fatalf("message.started 的 entry_id 应等于 assistant 条目 id：%q vs %q", msg.EntryID, assistID)
	}
	if text != "re:你好" || !ended {
		t.Fatalf("增量应拼成完整正文并以 message.ended 收尾：%q ended=%v", text, ended)
	}
	if len(userIDs) != 1 || userIDs[0] != "c1" {
		t.Fatalf("用户条目应记 client_ids [c1]：%v", userIDs)
	}
	for _, e := range evs {
		if !wire.Public(e.Kind) {
			t.Fatalf("不应推内部事件 %s", e.Kind)
		}
	}
	// 空闲时快照不带 live。
	out, e := call2(t, app, "session.get", `{"session":"s"}`)
	if e != nil {
		t.Fatal(e)
	}
	if s := out.(wire.SessionSnapshot); s.Live != nil || s.RunState != wire.RunIdle {
		t.Fatalf("空闲快照不应带 live：%+v", s)
	}
}

// live：步进行中有内容；新的一步清空、回合结束清空；重试丢弃旧段。
func TestLiveStateSnapshot(t *testing.T) {
	app := newApp(t)
	l := newLiveState(app.o.Bus, "s")
	if l.snapshot() != nil {
		t.Fatal("没有进行中的步不应有 live")
	}
	l.onStream(agentStep(1, "e1", 1))
	l.onStream(agentText("ab"))
	l.onStream(agentTool("c1", "echo", `{"s":`))
	lv := l.snapshot()
	if lv == nil || len(lv.Streams) != 1 || lv.Streams[0].Text != "ab" || lv.Streams[0].EntryID != "e1" {
		t.Fatalf("live 应带当前段：%+v", lv)
	}
	if len(lv.Tools) != 1 || lv.Tools[0].Name != "echo" || lv.Tools[0].Args != nil {
		t.Fatalf("半截参数不应作为 JSON 给出：%+v", lv.Tools)
	}
	l.onStream(agentReset(2))
	if lv := l.snapshot(); len(lv.Streams) != 0 || len(lv.Tools) != 0 {
		t.Fatalf("重试后旧段应丢弃：%+v", lv)
	}
	l.endTurn()
	if l.snapshot() != nil {
		t.Fatal("回合结束应清空 live")
	}
}

func agentStep(turn uint32, entry string, attempt int) agent.StreamEvent {
	return agent.StreamEvent{Kind: agent.StreamStep, Turn: turn, EntryID: entry, Attempt: attempt}
}
func agentText(s string) agent.StreamEvent { return agent.StreamEvent{Kind: agent.StreamText, Text: s} }
func agentTool(id, name, args string) agent.StreamEvent {
	return agent.StreamEvent{Kind: agent.StreamTool, CallID: id, Name: name, Args: args}
}
func agentReset(attempt int) agent.StreamEvent {
	return agent.StreamEvent{Kind: agent.StreamReset, Attempt: attempt}
}

// 工具执行中的输出 → tool.progress；工具结束后（或不认识的调用）到达的丢掉。
func TestToolProgress(t *testing.T) {
	app := newApp(t)
	sub := app.o.Bus.Subscribe(context.Background(), bus.Filter{Session: "s", Kinds: []string{wire.EvToolProgress, wire.EvToolEnded}})
	defer sub.Close()
	l := newLiveState(app.o.Bus, "s")
	l.onStream(agentStep(1, "e1", 1))
	l.onStream(agent.StreamEvent{Kind: agent.StreamProgress, CallID: "nope", Text: "x"}) // 不认识：丢
	l.onToolCall(session.ToolCall{ID: "c1", Name: "shell", Args: json.RawMessage(`{}`)})
	l.onStream(agent.StreamEvent{Kind: agent.StreamProgress, CallID: "c1", Text: "line 1\nline 2"})
	l.onToolResult(session.ToolCall{ID: "c1", Name: "shell"}, "done", false)
	l.onStream(agent.StreamEvent{Kind: agent.StreamProgress, CallID: "c1", Text: "late"}) // 已结束：丢
	ev := next(t, sub, wire.EvToolProgress)
	if p := ev.Data.(v2Event).Payload.(wire.ToolProgress); p.CallID != "c1" || p.Text != "line 1\nline 2" {
		t.Fatalf("tool.progress：%+v", p)
	}
	next(t, sub, wire.EvToolEnded)
	select {
	case e := <-sub.C():
		t.Fatalf("结束后不应再有事件：%+v", e)
	case <-time.After(100 * time.Millisecond):
	}
}
