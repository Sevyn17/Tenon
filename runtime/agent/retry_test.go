package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
)

// Retry 遇到只读 / 损坏的会话要报错，不能当成"没有可重跑的内容"静默结束
// （2026-11-04 审查 P1-22：所属树的重启策略据此区分）。
func TestRetryReportsReadOnly(t *testing.T) {
	c, err := session.Restore(session.Snapshot{Header: session.Header{Version: 2, Status: session.StatusIdle}})
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Core: c}
	if _, err := a.Retry(context.Background()); !errors.Is(err, session.ErrReadOnly) {
		t.Fatalf("只读会话 Retry 应报 ErrReadOnly，得 %v", err)
	}
	// 没有可续跑回合：仍按 Done 处理。
	a2 := &Agent{Core: session.New(&session.Config{Model: "m", Policy: session.DefaultPolicy()})}
	if end, err := a2.Retry(context.Background()); err != nil || end.Kind != session.EndDone {
		t.Fatalf("空会话 Retry 应 Done：%v %v", end, err)
	}
}
