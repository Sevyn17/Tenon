package host

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// countUser 数一组消息里文本等于 txt 的用户消息。
func countUser(ms []session.Message, txt string) int {
	n := 0
	for _, m := range ms {
		if m.Role != "user" {
			continue
		}
		s := ""
		for _, p := range m.Content {
			s += p.Text
		}
		if s == txt {
			n++
		}
	}
	return n
}

// storedHistory 按存储里已提交的日志重建对话消息（与内存 Committed() 分开核对）。
func storedHistory(t *testing.T, db store.Store, id string) []session.Message {
	t.Helper()
	ld, err := db.Load(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req, err := session.BuildRequest(session.Context{Config: ld.Snapshot.Header.Config, Items: ld.Snapshot.Entries, Base: ld.Snapshot.Base})
	if err != nil {
		t.Fatal(err)
	}
	return req.Messages
}

// checkOnce 核对 txt 在存储日志、内存 Committed() 里各只出现一次，信箱已清空。
func checkOnce(t *testing.T, db store.Store, hd Handle, txt string) {
	t.Helper()
	if n := countUser(messages(t, hd), txt); n != 1 {
		t.Errorf("Committed() 里 %s 出现 %d 次：%s", txt, n, roleLine(t, hd))
	}
	if n := countUser(storedHistory(t, db, "s1"), txt); n != 1 {
		t.Errorf("存储日志里 %s 出现 %d 次", txt, n)
	}
	if left, _ := db.Inbox(context.Background(), "s1"); len(left) != 0 {
		t.Errorf("信箱应已清空：%+v", left)
	}
}

// 回合中（工具执行时）普通 Send 插话：已提交日志、内存 Committed()、发给模型的请求里都只出现一次。
// 缺陷（2026-11-20）：步边界里 mailDirty 兜底读信箱插入一次，随后队列里的 Send 命令又 insertInput 一次；
// 第一次插入的消费只记在检查点缓冲里，信箱还读得到它。
func TestMidTurnSendInsertedOnce(t *testing.T) {
	started, gate := make(chan struct{}, 1), make(chan struct{})
	m := &fakeModel{}
	db := memstore.New()
	r := newT2Rig(t, db, m, 0, gatedTool(started, gate))
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: "a", Text: "use tool"}); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, started)
	if err := hd.Do(Command{Kind: Send, ItemID: "b", Text: "B"}); err != nil {
		t.Fatal(err)
	}
	close(gate)
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	checkOnce(t, db, hd, "B")
	m.mu.Lock()
	reqs := append([]session.Request(nil), m.reqs...)
	m.mu.Unlock()
	if n := countUser(reqs[len(reqs)-1].Messages, "B"); n != 1 {
		t.Errorf("模型最后一次请求里 B 出现 %d 次（共 %d 次请求）", n, len(reqs))
	}
	if got := roleLine(t, hd); got != "user:use tool | assistant: | tool:echoed | user:B | assistant:re:B" {
		t.Errorf("历史：%s", got)
	}
}

// 同一步边界里连续两条插话：各出现一次，按发送顺序合并进同一次请求。
func TestMidTurnTwoSendsInsertedOnce(t *testing.T) {
	started, gate := make(chan struct{}, 1), make(chan struct{})
	db := memstore.New()
	r := newT2Rig(t, db, &fakeModel{}, 0, gatedTool(started, gate))
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: "a", Text: "use tool"}); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, started)
	for _, s := range []string{"B", "C"} {
		if err := hd.Do(Command{Kind: Send, ItemID: s, Text: s}); err != nil {
			t.Fatal(err)
		}
	}
	close(gate)
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	got := roleLine(t, hd)
	if got != "user:use tool | assistant: | tool:echoed | user:B | user:C | assistant:re:C" {
		t.Fatalf("B、C 各应出现一次、按发送顺序：%s", got)
	}
	checkOnce(t, db, hd, "B")
	checkOnce(t, db, hd, "C")
	if left, _ := db.Inbox(context.Background(), "s1"); len(left) != 0 {
		t.Errorf("信箱应已清空：%+v", left)
	}
}

// 插话在模型调用进行中（两个检查点之间）到达、模型回复后回合直接结束：留给下一回合，只出现一次。
func TestSendDuringModelCallOnce(t *testing.T) {
	m := &fakeModel{block: make(chan struct{})}
	db := memstore.New()
	r := newT2Rig(t, db, m, 0, nil)
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: "a", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		n := m.calls
		m.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("超时：模型没被调用")
		}
		time.Sleep(time.Millisecond)
	}
	if err := hd.Do(Command{Kind: Send, ItemID: "b", Text: "B"}); err != nil {
		t.Fatal(err)
	}
	close(m.block)
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	checkOnce(t, db, hd, "B")
	if got := roleLine(t, hd); got != "user:hi | assistant:re:hi | user:B | assistant:re:B" {
		t.Errorf("历史：%s", got)
	}
}

// 工具执行中插话、随后取消回合：插话不丢（落进历史一次），信箱清空；下一回合模型能看到它一次。
func TestSendThenCancelOnce(t *testing.T) {
	started := make(chan struct{}, 1)
	m := &fakeModel{}
	db := memstore.New()
	r := newT2Rig(t, db, m, 0, gatedTool(started, make(chan struct{})))
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: "a", Text: "use tool"}); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, started)
	if err := hd.Do(Command{Kind: Send, ItemID: "b", Text: "B"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Do(Command{Kind: Cancel}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	checkOnce(t, db, hd, "B")
	if err := hd.Do(Command{Kind: Send, ItemID: "c", Text: "next"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	checkOnce(t, db, hd, "B")
	m.mu.Lock()
	last := m.reqs[len(m.reqs)-1]
	m.mu.Unlock()
	if n := countUser(last.Messages, "B"); n != 1 {
		t.Errorf("下一回合请求里 B 出现 %d 次", n)
	}
}

// 插话与回合结束赛跑（工具放行后立即发）：无论插进当前回合还是留给下一回合，都只出现一次、得到回复。
func TestSendRacingTurnEndOnce(t *testing.T) {
	for i := 0; i < 30; i++ {
		started, gate := make(chan struct{}, 1), make(chan struct{})
		db := memstore.New()
		r := newT2Rig(t, db, &fakeModel{}, 0, gatedTool(started, gate))
		hd := mustOpen(t, r.h, "s1")
		if err := hd.Do(Command{Kind: Send, ItemID: "a", Text: "use tool"}); err != nil {
			t.Fatal(err)
		}
		waitSignal(t, started)
		close(gate)
		if err := hd.Do(Command{Kind: Send, ItemID: "b", Text: "B"}); err != nil {
			t.Fatal(err)
		}
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
		checkOnce(t, db, hd, "B")
		if got := roleLine(t, hd); !strings.HasSuffix(got, "user:B | assistant:re:B") {
			t.Fatalf("第 %d 次：B 应得到回复：%s", i, got)
		}
	}
}
