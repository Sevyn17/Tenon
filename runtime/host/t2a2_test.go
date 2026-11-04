package host

// 09 9.4 T2a-2：空闲休眠 / 唤醒 / 兜底扫描（02 2.2）。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ybase/runtime/session"
	"ybase/runtime/store"
)

// status 读会话生命周期状态（测试辅助）。
func status(t *testing.T, r *t2Rig, id string) string {
	t.Helper()
	st, err := r.db.SessionStatus(context.Background(), id)
	if err != nil {
		t.Fatalf("读状态 %s: %v", id, err)
	}
	return st
}

// textOf 把历史里的消息文本拼起来（测试辅助）。
func textOf(msgs []session.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		for _, p := range m.Content {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// 09 9.4 T2a-2：空闲超时后释放进程内对象与租约；投递（Open）能唤醒并继续。
func TestIdleSleepsAndWakesOnOpen(t *testing.T) {
	m := &scriptedModel{}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, nil)
	r.h.SleepAfter = 60 * time.Millisecond
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, Text: "一"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return status(t, r, "s1") == store.StatusSleeping })
	waitFor(t, func() bool {
		_, ok := r.h.Get("s1")
		return !ok
	})
	// 投递路径：appapi 的 handle() 找不到 actor 就 Open，抢到租约即唤醒（02 2.2）。
	hd2 := mustOpen(t, r.h, "s1")
	if st := status(t, r, "s1"); st != store.StatusActive {
		t.Fatalf("唤醒后状态应为 active，got %s", st)
	}
	if err := hd2.Do(Command{Kind: Send, Text: "二"}); err != nil {
		t.Fatal(err)
	}
	if err := hd2.Wait(); err != nil {
		t.Fatal(err)
	}
	msgs := messages(t, hd2)
	if got := textOf(msgs); !strings.Contains(got, "二") {
		t.Fatalf("唤醒后应处理新消息，历史=%q", got)
	}
	if n := m.getCalls(); n != 2 {
		t.Fatalf("应各跑一轮，calls=%d", n)
	}
}

// 09 9.4 T2a-2：兜底扫描唤醒「有信件的休眠会话」，且只处理一次。
func TestScanOnceWakesSleepingWithMail(t *testing.T) {
	m := &scriptedModel{}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, nil)
	r.h.SleepAfter = 50 * time.Millisecond
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, Text: "一"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return status(t, r, "s1") == store.StatusSleeping })
	waitFor(t, func() bool {
		_, ok := r.h.Get("s1")
		return !ok
	})
	// 模拟另一节点投递：直接写 inbox，不经过本进程的 actor。
	body, _ := json.Marshal(session.Content{session.NewTextPart("三")})
	if _, err := r.db.Admit(context.Background(), "s1", store.InboxItem{
		ItemID: "item-3", Kind: store.InboxInput, Input: string(body), Trust: "trusted",
	}); err != nil {
		t.Fatal(err)
	}
	n, err := r.h.ScanOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应唤醒 1 个会话，got %d", n)
	}
	hd2 := mustOpen(t, r.h, "s1")
	if err := hd2.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := textOf(messages(t, hd2)); !strings.Contains(got, "三") {
		t.Fatalf("扫描唤醒后应处理信件，历史=%q", got)
	}
	if n := m.getCalls(); n != 2 {
		t.Fatalf("只应多跑一轮，calls=%d", n)
	}
	// 已 active：再扫不应重复开回合。
	n2, err := r.h.ScanOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("已唤醒的会话不应重复唤醒，got %d", n2)
	}
	if n := m.getCalls(); n != 2 {
		t.Fatalf("扫描不应多开回合，calls=%d", n)
	}
}

// 09 9.4 T2a-2：回合未结束不休眠。
func TestRunningTurnDoesNotSleep(t *testing.T) {
	m := &scriptedModel{start: make(chan int, 1), gate: make(chan struct{})}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, nil)
	r.h.SleepAfter = 40 * time.Millisecond
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, Text: "长任务"}); err != nil {
		t.Fatal(err)
	}
	waitStarted(t, m.start, "模型调用")
	time.Sleep(150 * time.Millisecond)
	if st := status(t, r, "s1"); st != store.StatusActive {
		t.Fatalf("回合进行中不应休眠，状态=%s", st)
	}
	close(m.gate)
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	// 回合结束后再过一会儿会正常休眠。
	waitFor(t, func() bool { return status(t, r, "s1") == store.StatusSleeping })
}

// 只有背景（context）信件的会话照常休眠；兜底扫描不因背景信件唤醒它（2026-11-04 审查 P1-16：
// 原先信箱非空就不睡，背景信件把会话钉在内存里，还会唤醒休眠会话）。
func TestContextOnlyInboxSleepsAndStaysAsleep(t *testing.T) {
	m := &scriptedModel{}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, nil)
	r.h.SleepAfter = 50 * time.Millisecond
	hd := mustOpen(t, r.h, "s1")
	body, _ := json.Marshal(session.Content{session.NewTextPart("背景")})
	if _, err := r.db.Admit(context.Background(), "s1", store.InboxItem{
		ItemID: "ctx-1", Kind: store.InboxContext, Input: string(body), Trust: "trusted",
	}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return status(t, r, "s1") == store.StatusSleeping })
	waitFor(t, func() bool {
		_, ok := r.h.Get("s1")
		return !ok
	})
	n, err := r.h.ScanOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("只有背景信件不应唤醒，got %d", n)
	}
	if c := m.getCalls(); c != 0 {
		t.Fatalf("背景信件不应开回合，calls=%d", c)
	}
}
