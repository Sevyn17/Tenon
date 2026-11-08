package askuser

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// ask_user 把发起调用的会话 id 交给提问方（2026-11-04 审查 P1-28：原先不带会话 id，
// 多会话界面分不清是哪个子智能体 / 成员在问）。
func TestAskUserPassesSessionID(t *testing.T) {
	var got string
	tool := AskUserIn(func(_ context.Context, sid, q string, _ []string) (string, error) {
		got = sid
		return "ok", nil
	})
	if _, err := tool.Call(context.Background(), &toolhub.Cx{SessionID: "L/m-alice", Tool: tool},
		json.RawMessage(`{"question":"用哪个？"}`)); err != nil {
		t.Fatal(err)
	}
	if got != "L/m-alice" {
		t.Fatalf("提问应带会话 id，得 %q", got)
	}
}
