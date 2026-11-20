package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// gatedTool echo 工具阻塞到 gate 关闭（或回合取消），开始时往 started 发一次信号。
func gatedTool(started chan struct{}, gate chan struct{}) func(context.Context, json.RawMessage) (toolhub.Output, error) {
	return func(ctx context.Context, _ json.RawMessage) (toolhub.Output, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-gate:
			return toolhub.Text("echoed"), nil
		case <-ctx.Done():
			return toolhub.Output{}, ctx.Err()
		}
	}
}

// roleLine 把已提交历史压成 "role:文本" 序列，便于比较回合边界。
func roleLine(t *testing.T, hd Handle) string {
	t.Helper()
	var out []string
	for _, m := range messages(t, hd) {
		txt := ""
		for _, p := range m.Content {
			txt += p.Text
		}
		out = append(out, m.Role+":"+txt)
	}
	return strings.Join(out, " | ")
}

func waitSignal(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("超时：工具没开始")
	}
}

// 回合中 Send{Queue} 不插入当前回合：工具结果后模型看到的是 tool 结果，回合结束后才单独开回合处理它。
// 对照：不带 Queue 的 Send 仍在下一次调模型前插入（原行为）。
func TestQueuedSendWaitsForTurnEnd(t *testing.T) {
	for _, queue := range []bool{true, false} {
		started, gate := make(chan struct{}, 1), make(chan struct{})
		r := newT2Rig(t, nil, &fakeModel{}, 0, gatedTool(started, gate))
		hd := mustOpen(t, r.h, "s1")
		if err := hd.Do(Command{Kind: Send, ItemID: "a", Text: "use tool"}); err != nil {
			t.Fatal(err)
		}
		waitSignal(t, started)
		if err := hd.Do(Command{Kind: Send, ItemID: "b", Text: "B", Queue: queue}); err != nil {
			t.Fatal(err)
		}
		if queue {
			q, err := r.h.Queued(context.Background(), "s1")
			if err != nil || len(q) != 1 || q[0].ItemID != "b" {
				t.Fatalf("排队列表：%+v %v", q, err)
			}
		}
		close(gate)
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
		got := roleLine(t, hd)
		if queue {
			want := "user:use tool | assistant: | tool:echoed | assistant:done | user:B | assistant:re:B"
			if got != want {
				t.Fatalf("queue=true 历史：\n got %s\nwant %s", got, want)
			}
		} else if want := "user:use tool | assistant: | tool:echoed | user:B | assistant:re:B"; got != want {
			// 原行为：插进当前回合，工具结果之后直接是 B（只一次），没有单独的 done 回合。
			t.Fatalf("queue=false 历史：\n got %s\nwant %s", got, want)
		}
		if q, _ := r.h.Queued(context.Background(), "s1"); len(q) != 0 {
			t.Fatalf("处理完不应还在排队：%+v", q)
		}
	}
}

// 多条排队按发送顺序逐条开回合；取消的那条不执行；已处理的取消失败。
func TestQueuedOrderAndUnqueue(t *testing.T) {
	started, gate := make(chan struct{}, 1), make(chan struct{})
	r := newT2Rig(t, nil, &fakeModel{}, 0, gatedTool(started, gate))
	hd := mustOpen(t, r.h, "s1")
	ctx := context.Background()
	if err := hd.Do(Command{Kind: Send, ItemID: "a", Text: "use tool"}); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, started)
	for _, s := range []string{"B", "C", "D"} {
		if err := hd.Do(Command{Kind: Send, ItemID: s, Text: s, Queue: true}); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := r.h.Unqueue(ctx, "s1", "C"); err != nil || !ok {
		t.Fatalf("取消 C：%v %v", ok, err)
	}
	if ok, _ := r.h.Unqueue(ctx, "s1", "C"); ok {
		t.Fatal("重复取消应失败")
	}
	if ok, _ := r.h.Unqueue(ctx, "s1", "a"); ok {
		t.Fatal("不是排队项，不能取消")
	}
	q, _ := r.h.Queued(ctx, "s1")
	if len(q) != 2 || q[0].ItemID != "B" || q[1].ItemID != "D" {
		t.Fatalf("排队列表：%+v", q)
	}
	close(gate)
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	want := "user:use tool | assistant: | tool:echoed | assistant:done | user:B | assistant:re:B | user:D | assistant:re:D"
	if got := roleLine(t, hd); got != want {
		t.Fatalf("历史：\n got %s\nwant %s", got, want)
	}
	if ok, _ := r.h.Unqueue(ctx, "s1", "B"); ok {
		t.Fatal("已处理的不能取消")
	}
	// 被取消的 C 同 id 重放仍被拒（行保留）。
	if err := hd.Do(Command{Kind: Send, ItemID: "C", Text: "C", Queue: true}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(roleLine(t, hd), "user:C") {
		t.Fatal("取消的 C 不应执行")
	}
}

// 模拟重启：排队后进程在回合中退出，换一个 Host 打开同一存储，排队的消息仍在并会执行。
func TestQueuedSurvivesRestart(t *testing.T) {
	db := memstore.New()
	started, gate := make(chan struct{}, 1), make(chan struct{})
	r1 := newT2Rig(t, db, &fakeModel{}, 0, gatedTool(started, gate))
	hd := mustOpen(t, r1.h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: "a", Text: "use tool"}); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, started)
	if err := hd.Do(Command{Kind: Send, ItemID: "b", Text: "B", Queue: true}); err != nil {
		t.Fatal(err)
	}
	// 先排 Close 再取消：回合收尾后先执行 Close，排队项留在信箱。
	if err := hd.Post(Command{Kind: Close}); err != nil {
		t.Fatal(err)
	}
	_ = hd.Do(Command{Kind: Cancel})
	select {
	case <-hd.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("旧 Host 没退出")
	}
	items, _ := db.Inbox(context.Background(), "s1")
	if len(items) != 1 || items[0].ItemID != "b" || items[0].Kind != store.InboxQueued {
		t.Fatalf("重启前排队项应在信箱：%+v", items)
	}
	r2 := newT2Rig(t, db, &fakeModel{}, 0, nil)
	hd2 := mustOpen(t, r2.h, "s1")
	if err := hd2.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := roleLine(t, hd2); !strings.HasSuffix(got, "user:B | assistant:re:B") {
		t.Fatalf("重启后应处理排队项：%s", got)
	}
	if items, _ := db.Inbox(context.Background(), "s1"); len(items) != 0 {
		t.Fatalf("处理后信箱应空：%+v", items)
	}
}

// 空闲时 Send{Queue} 与普通 Send 相同：立即开回合。
func TestQueuedSendWhenIdle(t *testing.T) {
	r := newT2Rig(t, nil, &fakeModel{}, 0, nil)
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: "x", Text: "hi", Queue: true}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := roleLine(t, hd); got != "user:hi | assistant:re:hi" {
		t.Fatalf("历史：%s", got)
	}
}
