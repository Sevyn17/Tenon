package sqlite

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

func TestReplayAndList(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	c := newCore()
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "s1", Title: "一"}, c.Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	n1, _ := c.Input(txt("你好"), nil)
	n := commitAll(t, db, "s1", c, 0, nil)
	_, _ = c.ModelDone(n1.Model.RequestID, n1.ID, session.AssistantItem{Content: txt("嗨")}, session.StopReason{})
	n = commitAll(t, db, "s1", c, n, nil)

	evs, wm, err := db.Replay(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Kind != store.EventEntry || evs[0].Seq != 1 || evs[1].Seq != 2 || wm != 2 {
		t.Fatalf("回放 %+v wm=%d", evs, wm)
	}
	if evs, _, _ := db.Replay(ctx, "s1", 1); len(evs) != 1 || evs[0].Seq != 2 {
		t.Fatalf("after=1 应只剩一条: %+v", evs)
	}

	// 截断：占一个 seq，回放里出现 rewind{to}
	s := c.Snapshot()
	one := 1
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "s1", ExpectLogLen: n, TruncateFrom: &one, AppendFrom: 1,
		Header: s.Header, RunState: "idle"}); err == nil {
		evs, wm, err := db.Replay(ctx, "s1", 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 || evs[0].Kind != store.EventRewind || evs[0].Seq != 3 || wm != 3 {
			t.Fatalf("回退事件 %+v wm=%d", evs, wm)
		}
		if to := evs[0].Data.(map[string]any)["to"]; to != 1 {
			t.Fatalf("to = %v", to)
		}
	} else {
		t.Fatal(err)
	}

	// 提交后通知：条目事件与回放一致
	var got []bus.Envelope
	db.OnCommit(func(e bus.Envelope) { got = append(got, e) })
	_, _ = c.Input(txt("再来"), nil)
	s = c.Snapshot()
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "s1", ExpectLogLen: 2, AppendFrom: 2,
		Append: s.Entries[2-s.Base:], Header: s.Header, RunState: "running"}); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Kind != store.EventEntry || got[0].Seq != 4 {
		t.Fatalf("提交通知 %+v", got)
	}
	db.OnCommit(nil)

	if err := db.CreateSession(ctx, store.SessionMeta{ID: "s1/sub-1", ParentID: "s1", Origin: "subagent"}, newCore().Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	all, err := db.ListSessions(ctx, 0, 10, false)
	if err != nil || len(all) != 2 {
		t.Fatalf("列表 %+v %v", all, err)
	}
	top, err := db.ListSessions(ctx, 0, 10, true)
	if err != nil || len(top) != 1 || top[0].ID != "s1" || top[0].Title != "一" {
		t.Fatalf("顶层列表 %+v %v", top, err)
	}
	if _, _, err := db.Replay(ctx, "none", 0); err == nil {
		t.Fatal("不存在的会话应报错")
	}
}
