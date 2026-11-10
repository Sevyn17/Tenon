package storetest

// 组件事件日志一致性用例（框架设计 04 4.6，T4b）。sqlite 与 memstore 共用，语义以 sqlite 为准。
// 存储没实现 EventLog 时跳过：事件日志是可选能力，与 CollectionStore 同一规则。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Sevyn17/tenon/runtime/store"
)

func eventLog(t *testing.T, db store.Store) store.EventLog {
	t.Helper()
	el, ok := db.(store.EventLog)
	if !ok {
		t.Skip("存储不支持事件日志")
	}
	return el
}

func appendEvent(t *testing.T, el store.EventLog, source, name, data string) int64 {
	t.Helper()
	seq, err := el.Append(context.Background(), store.ComponentEvent{Source: source, Name: name, Data: json.RawMessage(data)})
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func testEventLogRoundTrip(t *testing.T, db store.Store) {
	el := eventLog(t, db)
	ctx := context.Background()
	a := appendEvent(t, el, "workgroup", "task.created", `{"id":1}`)
	b := appendEvent(t, el, "workgroup", "task.updated", `{"id":1}`)
	if a != 1 || b != 2 {
		t.Fatalf("序号应从 1 起单调：%d %d", a, b)
	}
	got, err := el.Read(ctx, 0, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("Read: %v %+v", err, got)
	}
	if got[0].Seq != 1 || got[0].Source != "workgroup" || got[0].Name != "task.created" || string(got[0].Data) != `{"id":1}` {
		t.Fatalf("第一条不对：%+v", got[0])
	}
	if got[0].At == 0 {
		t.Fatal("At 为零时应由存储补上")
	}
	rest, err := el.Read(ctx, 1, 0)
	if err != nil || len(rest) != 1 || rest[0].Seq != 2 {
		t.Fatalf("after=1 应只剩第二条：%v %+v", err, rest)
	}
	if empty, err := el.Read(ctx, 2, 0); err != nil || len(empty) != 0 {
		t.Fatalf("读完应为空：%v %+v", err, empty)
	}
	// 缺省载荷写 {}。
	appendEvent(t, el, "workgroup", "task.done", "")
	last, err := el.Read(ctx, 2, 0)
	if err != nil || len(last) != 1 || string(last[0].Data) != `{}` {
		t.Fatalf("空载荷应写成 {}：%v %+v", err, last)
	}
}

func testEventLogCursor(t *testing.T, db store.Store) {
	el := eventLog(t, db)
	ctx := context.Background()
	if cur, err := el.Cursor(ctx, "audit.task.*"); err != nil || cur != 0 {
		t.Fatalf("没有记录应返回 0：%d %v", cur, err)
	}
	if err := el.SetCursor(ctx, "audit.task.*", 3); err != nil {
		t.Fatal(err)
	}
	if cur, err := el.Cursor(ctx, "audit.task.*"); err != nil || cur != 3 {
		t.Fatalf("读回应为 3：%d %v", cur, err)
	}
	if err := el.SetCursor(ctx, "audit.task.*", 3); err != nil {
		t.Fatalf("原地重写应允许：%v", err)
	}
	if err := el.SetCursor(ctx, "audit.task.*", 2); err == nil {
		t.Fatal("读位点不能退回")
	}
	if cur, _ := el.Cursor(ctx, "audit.task.*"); cur != 3 {
		t.Fatalf("拒绝后退后读位点应仍是 3：%d", cur)
	}
	// 另一个订阅者的读位点互不影响。
	if err := el.SetCursor(ctx, "other.task.*", 1); err != nil {
		t.Fatal(err)
	}
	if cur, _ := el.Cursor(ctx, "audit.task.*"); cur != 3 {
		t.Fatalf("别的订阅者不应改到这一条：%d", cur)
	}
}

func testEventLogTx(t *testing.T, db store.Store) {
	el := eventLog(t, db)
	ctx := context.Background()
	appendEvent(t, el, "workgroup", "task.created", `{"id":1}`)
	sentinel := errors.New("内部错误")
	err := db.Tx(ctx, func(tx store.Store) error {
		tel, ok := tx.(store.EventLog)
		if !ok {
			t.Fatal("事务视图应实现 EventLog")
		}
		if _, err := tel.Append(ctx, store.ComponentEvent{Source: "workgroup", Name: "task.updated", Data: json.RawMessage(`{"id":2}`)}); err != nil {
			return err
		}
		got, err := tel.Read(ctx, 0, 0)
		if err != nil || len(got) != 2 {
			t.Fatalf("事务内应看到自己追加的：%v %+v", err, got)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx 应传播 fn 的错误：%v", err)
	}
	got, err := el.Read(ctx, 0, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("回滚不应留下事务里追加的事件：%v %+v", err, got)
	}
	if err := db.Tx(ctx, func(tx store.Store) error {
		tel := tx.(store.EventLog)
		if _, err := tel.Append(ctx, store.ComponentEvent{Source: "workgroup", Name: "task.updated", Data: json.RawMessage(`{"id":3}`)}); err != nil {
			return err
		}
		return tel.SetCursor(ctx, "audit.task.*", 2)
	}); err != nil {
		t.Fatal(err)
	}
	got, err = el.Read(ctx, 0, 0)
	if err != nil || len(got) != 2 || got[1].Seq != 2 {
		t.Fatalf("提交后应可见且序号接上：%v %+v", err, got)
	}
	if cur, err := el.Cursor(ctx, "audit.task.*"); err != nil || cur != 2 {
		t.Fatalf("读位点应随事务提交：%d %v", cur, err)
	}
}

func testEventLogLimit(t *testing.T, db store.Store) {
	el := eventLog(t, db)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		appendEvent(t, el, "workgroup", "task.created", `{}`)
	}
	got, err := el.Read(ctx, 0, 2)
	if err != nil || len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("limit 应截断且保持顺序：%v %+v", err, got)
	}
	if _, err := el.Append(ctx, store.ComponentEvent{Source: "workgroup", Name: "BAD"}); err == nil {
		t.Fatal("非法事件名应拒绝")
	}
	if _, err := el.Append(ctx, store.ComponentEvent{Name: "task.created"}); err == nil {
		t.Fatal("缺来源应拒绝")
	}
}
