package component

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/ext"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// 事件订阅（T4b 第 2 步）。只走登记入口，不依赖真实组件。

// eventSink 订阅处理函数的收集器。
type eventSink struct {
	mu   sync.Mutex
	got  []store.ComponentEvent
	fail int // 前几次调用返回错误
}

func (s *eventSink) handle(ctx context.Context, e store.ComponentEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail > 0 {
		s.fail--
		return errors.New("暂时失败")
	}
	s.got = append(s.got, e)
	return nil
}

func (s *eventSink) wait(t *testing.T, n int) []store.ComponentEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.got) >= n {
			out := append([]store.ComponentEvent(nil), s.got...)
			s.mu.Unlock()
			return out
		}
		s.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("等到 %d 条，只收到 %d：%+v", n, len(s.got), s.got)
	return nil
}

func TestEventResumeAfterRestart(t *testing.T) {
	db := memstore.New()
	ctx := context.Background()
	if _, err := db.Append(ctx, store.ComponentEvent{Source: "workgroup", Name: "task.created", Data: json.RawMessage(`{"id":1}`)}); err != nil {
		t.Fatal(err)
	}

	sink := &eventSink{}
	p := collPlugin(t, &fake{meta: Meta{}, setup: func(cx *Registrar) error {
		return cx.On("task.*", sink.handle)
	}})
	start := func() *Scope {
		t.Helper()
		a, err := Assemble(Spec{Plugins: entries(p), Inject: map[string]any{ext.ServiceDB: dbHandle{db}}})
		if err != nil {
			t.Fatal(err)
		}
		a.eventBackoff = time.Millisecond
		root := NewRoot("t")
		if err := a.Start(root); err != nil {
			t.Fatal(err)
		}
		return root
	}
	root := start()
	got := sink.wait(t, 1)
	if got[0].Seq != 1 || string(got[0].Data) != `{"id":1}` {
		t.Fatalf("第一条不对：%+v", got[0])
	}
	// 投递成功后读位点才推进，关掉之前要等它记下，否则重启会把第一条再投一遍。
	deadline := time.Now().Add(2 * time.Second)
	for {
		cur, err := db.Cursor(ctx, p+".task.*")
		if err != nil {
			t.Fatal(err)
		}
		if cur == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("读位点没有记下：%d", cur)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := root.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// 停掉之后发布的，重启后从记下的读位点继续，第一条不重复。
	if _, err := db.Append(ctx, store.ComponentEvent{Source: "workgroup", Name: "task.updated", Data: json.RawMessage(`{"id":2}`)}); err != nil {
		t.Fatal(err)
	}
	root = start()
	defer root.Close(ctx)
	got = sink.wait(t, 2)
	if len(got) != 2 || got[1].Seq != 2 || got[1].Name != "task.updated" {
		t.Fatalf("重启应只补上新的一条：%+v", got)
	}
}

func TestEventFailureDoesNotBlockPublisher(t *testing.T) {
	db := memstore.New()
	ctx := context.Background()
	sink := &eventSink{fail: 1}
	p := collPlugin(t, &fake{meta: Meta{}, setup: func(cx *Registrar) error {
		return cx.On("task.*", sink.handle)
	}})
	a, err := Assemble(Spec{Plugins: entries(p), Inject: map[string]any{ext.ServiceDB: dbHandle{db}}})
	if err != nil {
		t.Fatal(err)
	}
	a.eventBackoff = time.Millisecond
	root := NewRoot("t")
	defer root.Close(ctx)
	if err := a.Start(root); err != nil {
		t.Fatal(err)
	}

	// 发布方只是追加，订阅者的失败不进这里的返回值。
	seq, err := db.Append(ctx, store.ComponentEvent{Source: "workgroup", Name: "task.created", Data: json.RawMessage(`{"id":1}`)})
	if err != nil || seq != 1 {
		t.Fatalf("发布应成功：%d %v", seq, err)
	}
	if _, err := db.Append(ctx, store.ComponentEvent{Source: "workgroup", Name: "task.updated", Data: json.RawMessage(`{"id":2}`)}); err != nil {
		t.Fatal(err)
	}
	got := sink.wait(t, 2)
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("失败重试后两条都应送达：%+v", got)
	}
}

func TestEventFailureSkipsAndContinues(t *testing.T) {
	db := memstore.New()
	ctx := context.Background()
	sink := &eventSink{fail: EventRetries + 1}
	p := collPlugin(t, &fake{meta: Meta{}, setup: func(cx *Registrar) error {
		return cx.On("task.created", sink.handle)
	}})
	a, err := Assemble(Spec{Plugins: entries(p), Inject: map[string]any{ext.ServiceDB: dbHandle{db}}})
	if err != nil {
		t.Fatal(err)
	}
	a.eventBackoff = time.Millisecond
	root := NewRoot("t")
	defer root.Close(ctx)
	if err := a.Start(root); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Append(ctx, store.ComponentEvent{Source: "workgroup", Name: "task.created", Data: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	got := sink.wait(t, 1)
	if got[0].Seq != 2 {
		t.Fatalf("第一条用尽重试后应跳过，第二条照常送达：%+v", got)
	}
}

func TestEventDuplicateSubscriptionRejected(t *testing.T) {
	p := collPlugin(t, &fake{meta: Meta{}, setup: func(cx *Registrar) error {
		if err := cx.On("task.created", func(context.Context, store.ComponentEvent) error { return nil }); err != nil {
			return err
		}
		return cx.On("task.created", func(context.Context, store.ComponentEvent) error { return nil })
	}})
	if _, err := Assemble(Spec{Plugins: entries(p)}); err == nil {
		t.Fatal("同一组件重复订阅应拒绝")
	}
}
