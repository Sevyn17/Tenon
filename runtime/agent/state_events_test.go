package agent

import (
	"context"
	"encoding/json"
	"testing"

	"tenon/runtime/session"
	"tenon/runtime/store"
	"tenon/runtime/store/memstore"
)

// 会话状态的每个变化都是持久事件（protocol 2，06 10.2.2 规则 2）：运行状态、Ext、设置变化随检查点写进事件表，
// 没变的不写。
func TestCheckpointWritesStateEvents(t *testing.T) {
	ctx := context.Background()
	db := memstore.New()
	a := &Agent{Store: db, SessionID: "s", Model: fakeModel{}}
	if _, err := a.Open(ctx, testCfg()); err != nil {
		t.Fatal(err)
	}
	kinds := func(after int64) []string {
		t.Helper()
		evs, _, err := db.Replay(ctx, "s", after)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range evs {
			out = append(out, e.Kind)
		}
		return out
	}
	if err := a.Core.SetExt("permission.mode", "full"); err != nil {
		t.Fatal(err)
	}
	n := uint32(5)
	if _, err := a.Core.SetConfig(session.ConfigPatch{MaxSteps: &n}); err != nil {
		t.Fatal(err)
	}
	if err := a.CommitRun(ctx, "suspended"); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, k := range kinds(0) {
		got[k]++
	}
	if got[store.EventRunState] != 1 || got[store.EventSettings] != 1 || got[store.EventExt] != 1 {
		t.Fatalf("应各写一条运行状态 / 设置 / Ext 事件：%v", got)
	}
	evs, _, _ := db.Replay(ctx, "s", 0)
	for _, e := range evs {
		if e.Kind == store.EventExt {
			var d struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			}
			b, _ := json.Marshal(e.Data)
			_ = json.Unmarshal(b, &d)
			if d.Key != "permission.mode" || d.Value != "full" {
				t.Fatalf("Ext 事件内容不对：%s", b)
			}
		}
	}
	_, wm, _ := db.Replay(ctx, "s", 0)
	// 没有变化的提交不写状态事件。
	if err := a.CommitRun(ctx, "suspended"); err != nil {
		t.Fatal(err)
	}
	if ks := kinds(wm); len(ks) != 0 {
		t.Fatalf("状态未变不应写事件：%v", ks)
	}
}
