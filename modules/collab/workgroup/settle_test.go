package workgroup

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"ybase/runtime/agent"
	"ybase/runtime/session"
)

// 成员结果投递失败后在后台退避重试，长驻进程里不必等重启 Recover（2026-11-04 审查 P1-27）。
func TestSettleRetriesInBackground(t *testing.T) {
	oldB, oldM := settleBackoff, settleMaxBackoff
	settleBackoff, settleMaxBackoff = 10*time.Millisecond, 20*time.Millisecond
	defer func() { settleBackoff, settleMaxBackoff = oldB, oldM }()

	g, h, _, m := rig(t)
	inner := h.New
	var fails atomic.Int32
	fails.Store(2)
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		if id == "L" && fails.Add(-1) >= 0 { // 领导会话前两次打开失败：投递失败
			return nil, nil, errors.New("暂时打不开")
		}
		return inner(id, def)
	}
	if err := g.start("L", MemberID("L", "alice"), "alice", "查资料", false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !m.has("[成员 alice 完成]") {
		if time.Now().After(deadline) {
			t.Fatalf("投递失败后应重试直到领导收到结果：%v", m.seen)
		}
		if hd, ok := h.Get("L"); ok {
			_ = hd.Wait()
		}
		time.Sleep(20 * time.Millisecond)
	}
	if left, _ := g.SQL.DB().QueryContext(context.Background(), `SELECT 1 FROM p_workgroup_runs`); left != nil {
		defer left.Close()
		if left.Next() {
			t.Fatal("送达后应销账")
		}
	}
}
