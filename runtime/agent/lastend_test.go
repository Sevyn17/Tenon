package agent

// 回合结束原因（客户端改进 M2）：码与说明分开（N-5）、截断、随回合结束的检查点落库并写进 run_state 事件。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// noCredModel 模拟没配凭证：每次调用都返回 MISSING_CREDENTIAL。
type noCredModel struct{}

func (noCredModel) Stream(context.Context, session.Request, func(string)) (session.AssistantItem, session.StopReason, error) {
	return session.AssistantItem{}, session.StopReason{}, &modelhub.Failure{Code: modelhub.CodeMissingCred,
		Message: "提供方 deepseek 凭证 env:DEEPSEEK_API_KEY 不可用"}
}

func TestErrorReasonSplitsCode(t *testing.T) {
	f := &modelhub.Failure{Code: modelhub.CodeMissingCred, Message: "提供方 deepseek 凭证 env:DEEPSEEK_API_KEY 不可用"}
	for _, c := range []struct {
		why       string
		err       error
		fallback  string
		code, msg string
	}{
		{"modelhub.Failure", f, "", "MISSING_CREDENTIAL", "提供方 deepseek 凭证 env:DEEPSEEK_API_KEY 不可用"},
		{"包了一层的 Failure", fmt.Errorf("调模型：%w", f), "", "MISSING_CREDENTIAL", "提供方 deepseek 凭证 env:DEEPSEEK_API_KEY 不可用"},
		{"普通错误用兜底码", errors.New("磁盘满了"), "", CodeTurnFailed, "磁盘满了"},
		{"指定兜底码", errors.New("x"), "STORE", "STORE", "x"},
	} {
		r := ErrorReason(c.err, c.fallback)
		if r.Kind != session.EndFailed || r.Code != c.code || r.Message != c.msg {
			t.Errorf("%s：%+v，期望 code=%q message=%q", c.why, r, c.code, c.msg)
		}
	}
	long := strings.Repeat("密", 400) // 1200 字节
	r := ErrorReason(errors.New(long), "")
	if len(r.Message) > store.LastEndMessageMax || !utf8.ValidString(r.Message) || !strings.HasSuffix(r.Message, "…") {
		t.Fatalf("长说明应按字符边界截到 %d 字节以内并带省略号：len=%d valid=%v", store.LastEndMessageMax,
			len(r.Message), utf8.ValidString(r.Message))
	}
}

// 模型立即失败：结束原因 Failed，码与说明分开；和 run_state=idle 一起落库，run_state 事件带 last_end。
// 下一回合正常结束时被覆盖成 done。
func TestModelFailureRecordsLastEnd(t *testing.T) {
	ctx := context.Background()
	db := tempStore(t)
	a := newAgent(t, db, agentOpts{model: noCredModel{}})
	reason, err := a.Send(ctx, "你好")
	if err != nil {
		t.Fatal(err)
	}
	if reason.Kind != session.EndFailed || reason.Code != "MISSING_CREDENTIAL" ||
		reason.Message != "提供方 deepseek 凭证 env:DEEPSEEK_API_KEY 不可用" {
		t.Fatalf("结束原因应码与说明分开：%+v", reason)
	}
	l, err := db.Load(ctx, "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.RunState != "idle" || l.LastEnd == nil || l.LastEnd.Turn != 1 || l.LastEnd.End != reason || l.LastEnd.At == 0 {
		t.Fatalf("会话行：run=%q last_end=%+v", l.RunState, l.LastEnd)
	}
	evs, _, err := db.Replay(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got *store.LastEnd
	for _, e := range evs {
		if e.Kind != store.EventRunState {
			continue
		}
		var d struct {
			RunState string         `json:"run_state"`
			LastEnd  *store.LastEnd `json:"last_end"`
		}
		raw, _ := json.Marshal(e.Data)
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		if d.LastEnd != nil {
			if d.RunState != "idle" {
				t.Fatalf("带 last_end 的 run_state 应为 idle：%s", raw)
			}
			got = d.LastEnd
		}
	}
	if got == nil || *got != *l.LastEnd {
		t.Fatalf("run_state 事件应带与会话行相同的 last_end：%+v vs %+v", got, l.LastEnd)
	}

	a2 := newAgent(t, db, agentOpts{})
	if r, err := a2.Send(ctx, "再来"); err != nil || r.Kind != session.EndDone {
		t.Fatalf("第二回合：%s %v", r, err)
	}
	l, _ = db.Load(ctx, "s1", nil)
	if l.LastEnd == nil || l.LastEnd.Turn != 2 || l.LastEnd.End.Kind != session.EndDone {
		t.Fatalf("第二回合结束后 last_end 应为 done：%+v", l.LastEnd)
	}
}
