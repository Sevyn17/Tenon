package agent

// 故障注入（M1.12）：在每个 checkpoint 点之后"杀进程"（返回 errCrash，丢弃内存状态），
// 用同一存储重新打开并恢复，检查：回合能续跑完成、有副作用的工具不重复执行、
// 恢复 WaitingModel 时按请求记录原样重发。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
)

func TestCrashRecovery(t *testing.T) {
	cases := []struct {
		point      string
		wantRuns   int    // echo 实际执行次数（跨崩溃前后合计）
		wantPend   bool   // 重新打开后是否有未完成回合
		wantText   string // 恢复后模型最终输出（包含）
		wantResent bool   // 恢复时是否按请求记录重组
	}{
		{point: "before_model", wantRuns: 1, wantPend: true, wantText: "工具说：pong", wantResent: true},
		{point: "before_tool", wantRuns: 1, wantPend: true, wantText: "工具说：pong"},
		{point: "tool_started", wantRuns: 0, wantPend: true, wantText: "结果未知"},
		{point: "tool_ran", wantRuns: 1, wantPend: true, wantText: "结果未知"},
		{point: "turn_end", wantRuns: 1, wantPend: false},
	}
	for _, tc := range cases {
		t.Run(tc.point, func(t *testing.T) {
			db := openStore(t)
			runs := 0
			a1 := newAgent(t, db, agentOpts{runs: &runs, crash: tc.point})
			if _, err := a1.Send(context.Background(), "ping"); !errors.Is(err, errCrash) {
				t.Fatalf("应在 %s 崩溃: %v", tc.point, err)
			}
			// 同一份已提交的存储上换一个执行体。文件级关库重开由存储实现方验证。
			a2 := newAgent(t, db, agentOpts{runs: &runs})
			if a2.Pending() != tc.wantPend {
				t.Fatalf("Pending=%v，期望 %v", a2.Pending(), tc.wantPend)
			}
			if (a2.resumed.req != nil) != tc.wantResent {
				t.Fatalf("请求记录重组=%v，期望 %v", a2.resumed.req != nil, tc.wantResent)
			}
			var text strings.Builder
			a2.Hooks.OnText = func(d string) { text.WriteString(d) }
			reason, err := a2.Recover(context.Background())
			if err != nil {
				t.Fatalf("Recover: %v", err)
			}
			if reason.Kind != session.EndDone {
				t.Fatalf("恢复后回合应完成: %s", reason)
			}
			if runs != tc.wantRuns {
				t.Fatalf("echo 执行 %d 次，期望 %d（有副作用的工具不得重复执行）", runs, tc.wantRuns)
			}
			if !strings.Contains(text.String(), tc.wantText) {
				t.Fatalf("恢复输出 %q 不含 %q", text.String(), tc.wantText)
			}
			if a2.Core.Status() != session.StatusIdle {
				t.Fatal("恢复后应回到 Idle")
			}
			// 续跑完的会话还能开新回合。
			if r, err := a2.Send(context.Background(), "again"); err != nil || r.Kind != session.EndDone {
				t.Fatalf("新回合: %s %v", r, err)
			}
		})
	}
}

// 第二个写者：存储已被另一实例推进，条件写失败（session-host D4）。
func TestSecondWriterConflicts(t *testing.T) {
	db := tempStore(t)
	a1 := newAgent(t, db, agentOpts{})
	a2 := newAgent(t, db, agentOpts{}) // 同时打开同一会话
	if _, err := a1.Send(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	if _, err := a2.Send(context.Background(), "ping"); err == nil || !strings.Contains(err.Error(), "条件写冲突") {
		t.Fatalf("第二个写者应条件写失败: %v", err)
	}
}
