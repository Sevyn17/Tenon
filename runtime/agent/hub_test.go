package agent

// M4.4 / 4.9：工具走 toolhub。等确认时杀进程，恢复后同一调用仍在等确认（不执行、不丢）；
// 批准后执行一次；拒绝回给模型。

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/modules/storage/sqlite"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func hubAgent(t *testing.T, db *sqlite.DB, runs *int, ask Ask) *Agent {
	t.Helper()
	return newAgent(t, db, agentOpts{runs: runs, ask: ask})
}

func TestHubConfirmSurvivesCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	runs := 0

	db1 := openStore(t, path)
	a1 := hubAgent(t, db1, &runs, func(context.Context, *toolhub.Cx, string, time.Time) bool { return true })
	a1.crashAt = "awaiting_confirm"
	if _, err := a1.Send(context.Background(), "ping"); !errors.Is(err, errCrash) {
		t.Fatalf("应在等确认时崩溃: %v", err)
	}
	db1.Close()

	db2 := openStore(t, path)
	asked := 0
	a2 := hubAgent(t, db2, &runs, func(context.Context, *toolhub.Cx, string, time.Time) bool { asked++; return true })
	if !a2.Pending() {
		t.Fatal("恢复后应有未完成回合")
	}
	var text strings.Builder
	a2.Hooks.OnText = func(d string) { text.WriteString(d) }
	r, err := a2.Recover(context.Background())
	if err != nil || r.Kind != session.EndDone {
		t.Fatalf("Recover: %s %v", r, err)
	}
	if asked != 1 || runs != 1 || !strings.Contains(text.String(), "pong") {
		t.Fatalf("恢复后应重新询问一次并执行一次: asked=%d runs=%d text=%q", asked, runs, text.String())
	}
}

func TestHubConfirmDenied(t *testing.T) {
	runs := 0
	db := openStore(t, filepath.Join(t.TempDir(), "state.db"))
	a := hubAgent(t, db, &runs, func(context.Context, *toolhub.Cx, string, time.Time) bool { return false })
	var text strings.Builder
	a.Hooks.OnText = func(d string) { text.WriteString(d) }
	if _, err := a.Send(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || !strings.Contains(text.String(), "拒绝") {
		t.Fatalf("拒绝后不应执行，且拒绝结果交给模型: runs=%d text=%q", runs, text.String())
	}
}

// askLayer 测试用的“每次询问” Guard：只读放行，其余未批准即要确认（内核测试不依赖权限插件）。
func askGate() toolhub.Gate {
	// 04 4.3.2：纯判定（只读放行、其余要确认），所以是 Gate 不是 Transform。
	return toolhub.Gate{Name: "permission",
		Check: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage) (toolhub.Verdict, string) {
			if cx.Approved || (cx.Tool != nil && cx.Tool.Risk == toolhub.Read) {
				return toolhub.VerdictAllow, ""
			}
			return toolhub.VerdictConfirm, cx.Tool.Spec.Name + " 需要确认"
		}}
}
