package host

// 回合结束原因（客户端改进 M2 / C-1）：drive 直接返回 error 时也发 turn_end（原先只发内部的 turn.failed），
// 结束原因与 run_state=idle 一起落库；每个回合只发一次 turn_end。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// noCredModel 模拟没配凭证：每次调用都返回 MISSING_CREDENTIAL。
type noCredModel struct{}

func (noCredModel) Stream(context.Context, session.Request, func(string)) (session.AssistantItem, session.StopReason, error) {
	return session.AssistantItem{}, session.StopReason{}, &modelhub.Failure{Code: modelhub.CodeMissingCred,
		Message: "提供方 deepseek 凭证 env:DEEPSEEK_API_KEY 不可用"}
}

// lastEndHost 宿主 + 总线。智能体没有工具管理器（Hub 为空）：模型一调工具 drive 就返回 error。
func lastEndHost(t *testing.T, m agent.Model) (*Host, store.Store, *bus.Sub) {
	t.Helper()
	db := memstore.New()
	b := bus.New(0)
	h := &Host{DB: db, Bus: b}
	t.Cleanup(func() { h.Close(); b.Close(); db.Close() })
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		a := &agent.Agent{Store: db, SessionID: id, Model: m}
		cfg := &session.Config{Model: "m", Tools: []session.ToolSpec{{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)}},
			Policy: session.DefaultPolicy()}
		return a, cfg, nil
	}
	sub := b.Subscribe(context.Background(), bus.Filter{Kinds: []string{"turn_start", "turn_end"}})
	return h, db, sub
}

// sendUntilExit 发一条消息，等 actor 因回合失败退出，返回退出错误。
func sendUntilExit(t *testing.T, h *Host, text string) error {
	t.Helper()
	hd := mustOpen(t, h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: text, Source: "user", Trust: "trusted"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hd.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("回合失败后 actor 应退出")
	}
	return hd.Err()
}

// drain 取出已发布的事件（actor 退出时已全部发布；bus.Publish 同步入队）。
func drain(sub *bus.Sub) []bus.Envelope {
	var out []bus.Envelope
	for {
		select {
		case e := <-sub.C():
			out = append(out, e)
		case <-time.After(200 * time.Millisecond):
			return out
		}
	}
}

// num 总线事件数据里的整数（发布时是 Go 原值：int / uint32）。
func num(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case uint32:
		return int(n)
	}
	return -1
}

func turnEnds(evs []bus.Envelope) []map[string]any {
	var out []map[string]any
	for _, e := range evs {
		if e.Kind == "turn_end" {
			out = append(out, e.Data.(map[string]any))
		}
	}
	return out
}

// drive 返回 error（这里是模型调了工具、却没有工具管理器）：补发 turn_end，结束原因带兜底码与原始错误，
// 与 run_state=idle 一起落库，提交之后才发 turn_end。
func TestDriveErrorPublishesTurnEnd(t *testing.T) {
	h, db, sub := lastEndHost(t, &fakeModel{})
	err := sendUntilExit(t, h, "调 tool")
	if err == nil || !strings.Contains(err.Error(), "工具管理器") {
		t.Fatalf("顶层会话应把 drive 的错误原样交回：%v", err)
	}
	ends := turnEnds(drain(sub))
	if len(ends) != 1 {
		t.Fatalf("drive 返回 error 时应发且只发一次 turn_end：%v", ends)
	}
	e := ends[0]
	if session.EndKind(num(e["end_kind"])) != session.EndFailed || e["code"] != agent.CodeTurnFailed ||
		!strings.Contains(e["message"].(string), "工具管理器") || num(e["turn"]) != 1 {
		t.Fatalf("turn_end 应为 Failed(TURN_FAILED)，说明是原始错误：%v", e)
	}
	l, lerr := db.Load(context.Background(), "s1", nil)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if l.RunState != "idle" || l.Snapshot.Header.Status != session.StatusIdle {
		t.Fatalf("失败的一轮应收尾为 idle：run=%q status=%v", l.RunState, l.Snapshot.Header.Status)
	}
	if l.LastEnd == nil || l.LastEnd.Turn != 1 || l.LastEnd.End.Kind != session.EndFailed ||
		l.LastEnd.End.Code != agent.CodeTurnFailed || l.LastEnd.End.Message != e["message"] {
		t.Fatalf("last_end 应与 turn_end 一致并已落库：%+v", l.LastEnd)
	}
}

// 模型失败（drive 正常返回 Failed，再经 turnErr 交给 turnFailed）：turn_end 只发一次，码与说明分开；
// turnFailed 不拿 TURN_FAILED 覆盖已记下的 MISSING_CREDENTIAL。
func TestModelFailureTurnEndOnce(t *testing.T) {
	h, db, sub := lastEndHost(t, noCredModel{})
	err := sendUntilExit(t, h, "你好")
	if err == nil || !strings.Contains(err.Error(), "MISSING_CREDENTIAL: 提供方 deepseek") {
		t.Fatalf("退出错误应带码与说明：%v", err)
	}
	ends := turnEnds(drain(sub))
	if len(ends) != 1 {
		t.Fatalf("turn_end 应只发一次：%v", ends)
	}
	if ends[0]["code"] != "MISSING_CREDENTIAL" || ends[0]["message"] != "提供方 deepseek 凭证 env:DEEPSEEK_API_KEY 不可用" {
		t.Fatalf("turn_end 的码与说明应分开：%v", ends[0])
	}
	l, _ := db.Load(context.Background(), "s1", nil)
	if l.LastEnd == nil || l.LastEnd.End.Code != "MISSING_CREDENTIAL" || l.LastEnd.Turn != 1 {
		t.Fatalf("last_end 应保留模型给的码：%+v", l.LastEnd)
	}
}
