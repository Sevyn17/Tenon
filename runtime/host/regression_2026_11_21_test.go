package host

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/store"
)

// hostFaultStore只覆盖测试要控制的存储点位，其余能力原样委托内存存储。
type hostFaultStore struct {
	Store
	openInteractionFn func(context.Context, store.Pending, int64) error
	issueCorrFn       func(context.Context, store.Pending) error
	acquireLeaseFn    func(context.Context, string, string) (int64, error)
	resolveFn         func(context.Context, string, store.Resolution) (store.Pending, bool, error)
}

func (s *hostFaultStore) OpenInteraction(ctx context.Context, p store.Pending, epoch int64) error {
	if s.openInteractionFn != nil {
		return s.openInteractionFn(ctx, p, epoch)
	}
	return s.Store.OpenInteraction(ctx, p, epoch)
}

func (s *hostFaultStore) IssueCorr(ctx context.Context, p store.Pending) error {
	if s.issueCorrFn != nil {
		return s.issueCorrFn(ctx, p)
	}
	return s.Store.IssueCorr(ctx, p)
}

func (s *hostFaultStore) AcquireLease(ctx context.Context, id, holder string) (int64, error) {
	if s.acquireLeaseFn != nil {
		return s.acquireLeaseFn(ctx, id, holder)
	}
	return s.Store.AcquireLease(ctx, id, holder)
}

func (s *hostFaultStore) ResolveInteraction(ctx context.Context, id string, r store.Resolution) (store.Pending, bool, error) {
	if s.resolveFn != nil {
		return s.resolveFn(ctx, id, r)
	}
	return s.Store.ResolveInteraction(ctx, id, r)
}

// OpenInteraction失败、IssueCorr成功时，corr-only确认仍必须能经信封回信完成。
func TestApprovalCorrFallbackAcceptsEnvelope(t *testing.T) {
	h, db := newHumanRig(t, &scriptedModel{firstTool: true})
	h.DB = &hostFaultStore{Store: db, openInteractionFn: func(context.Context, store.Pending, int64) error {
		return errors.New("open failed")
	}}
	ctx := context.Background()
	sub := h.Bus.Subscribe(ctx, bus.Filter{Kinds: []string{"human_requested"}})
	hd := mustOpen(t, h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "跑工具"}); err != nil {
		t.Fatal(err)
	}
	var corr, callID, iid string
	select {
	case ev := <-sub.C():
		data, _ := ev.Data.(map[string]any)
		corr, _ = data["corr"].(string)
		callID, _ = data["call_id"].(string)
		iid, _ = data["interaction"].(string)
	case <-time.After(5 * time.Second):
		t.Fatal("没收到 human_requested")
	}
	if corr == "" || callID == "" || iid != "" {
		t.Fatalf("应为 corr-only降级确认：corr=%q call_id=%q interaction=%q", corr, callID, iid)
	}
	reply := mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindReply, Corr: corr, Body: "approve"}
	if err := h.Deliver(ctx, reply); err != nil {
		t.Fatalf("Deliver回信: %v", err)
	}
	if items, err := db.Inbox(ctx, "s1"); err != nil || len(items) != 0 {
		//原实现会把回信落 inbox并继续空等；先用 Confirm解开工具，再报告失败。
		_ = hd.Do(Command{Kind: Confirm, CallID: callID, Approve: true})
		t.Fatalf("降级回信不应变成普通 inbox输入：items=%d err=%v", len(items), err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatalf("批准后回合应完成: %v", err)
	}
}

// 两种登记都失败时，Interact必须按原始 corr删除 askWait。
func TestInteractDoubleFailureCleansAskWait(t *testing.T) {
	h, db := newHumanRig(t, &scriptedModel{})
	mustOpen(t, h, "s1")
	h.DB = &hostFaultStore{Store: db,
		openInteractionFn: func(context.Context, store.Pending, int64) error { return errors.New("open failed") },
		issueCorrFn:       func(context.Context, store.Pending) error { return errors.New("issue failed") },
	}
	if _, err := h.Interact(context.Background(), "s1", Request{Kind: "question", Title: "问题"}); err == nil {
		t.Fatal("双失败应返回登记错误")
	}
	h.humanMu.Lock()
	left := len(h.askWait)
	h.humanMu.Unlock()
	if left != 0 {
		t.Fatalf("双失败后 askWait泄漏 %d个等待者", left)
	}
}

// 工具确认双失败仍保留 Confirm命令路径，但不能把原始 corr留在 humanWait。
func TestApprovalDoubleFailureCleansHumanWait(t *testing.T) {
	h, db := newHumanRig(t, &scriptedModel{firstTool: true})
	h.DB = &hostFaultStore{Store: db,
		openInteractionFn: func(context.Context, store.Pending, int64) error { return errors.New("open failed") },
		issueCorrFn:       func(context.Context, store.Pending) error { return errors.New("issue failed") },
	}
	ctx := context.Background()
	sub := h.Bus.Subscribe(ctx, bus.Filter{Kinds: []string{"human_requested"}})
	hd := mustOpen(t, h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "跑工具"}); err != nil {
		t.Fatal(err)
	}
	var callID string
	select {
	case ev := <-sub.C():
		data, _ := ev.Data.(map[string]any)
		callID, _ = data["call_id"].(string)
	case <-time.After(5 * time.Second):
		t.Fatal("没收到 human_requested")
	}
	if callID == "" {
		t.Fatal("双失败仍应发布 call_id")
	}
	if err := hd.Do(Command{Kind: Confirm, CallID: callID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	h.humanMu.Lock()
	left := len(h.humanWait)
	h.humanMu.Unlock()
	if left != 0 {
		t.Fatalf("双失败确认退出后 humanWait泄漏 %d个等待者", left)
	}
}

// AcquireLease在 actor已注册后失败：经 Get分发的 Handle不得永久卡在 Wait。
func TestAcquireLeaseFailureClosesDistributedHandle(t *testing.T) {
	h, db := newHumanRig(t, &scriptedModel{})
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	leaseErr := errors.New("lease failed")
	h.DB = &hostFaultStore{Store: db, acquireLeaseFn: func(context.Context, string, string) (int64, error) {
		close(entered)
		<-release
		return int64(0), leaseErr
	}}
	openDone := make(chan error, 1)
	go func() {
		_, err := h.Open(context.Background(), "s1")
		openDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("打开没有进入 AcquireLease")
	}
	hd, ok := h.Get("s1")
	if !ok {
		t.Fatal("AcquireLease阻塞期间应能取得已注册 Handle")
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- hd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for len(hd.a.in) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(hd.a.in) == 0 {
		unblock()
		<-openDone
		hd.a.once.Do(func() { close(hd.a.exit) })
		t.Fatal("Wait没有进入 actor队列")
	}
	unblock()
	if err := <-openDone; !errors.Is(err, leaseErr) {
		t.Fatalf("Open错误=%v，期望 %v", err, leaseErr)
	}
	select {
	case err := <-waitDone:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Wait应返回 ErrClosed，实际 %v", err)
		}
	case <-time.After(time.Second):
		//让失败实现下的测试自身也能收尾，不留下永久阻塞的 goroutine。
		hd.a.once.Do(func() { close(hd.a.exit) })
		<-waitDone
		t.Fatal("AcquireLease失败后 Wait永久阻塞")
	}
}

type interactResult struct {
	ans Answer
	err error
}

type respondResult struct {
	p   store.Pending
	won bool
	err error
}

// 显式固定顺序：Respond已查到等待者并阻塞在 Resolve；随后等待方取消并清理；最后 Resolve胜出。
// 修复后若取消先完成，Respond必须 voided；若 Respond持锁先完成，则取消分支必须收回已送达答案。
func TestRespondResolveRaceDoesNotLoseAnswer(t *testing.T) {
	h, db := newHumanRig(t, &scriptedModel{})
	mustOpen(t, h, "s1")
	entered := make(chan struct{})
	release := make(chan struct{})
	h.DB = &hostFaultStore{Store: db, resolveFn: func(ctx context.Context, id string, r store.Resolution) (store.Pending, bool, error) {
		p, won, err := db.ResolveInteraction(ctx, id, r)
		if r.Outcome == OutcomeAnswered && won {
			//存储已判定 Respond胜出，但 Respond尚未向等待通道送达。
			close(entered)
			<-release
		}
		return p, won, err
	}}
	ctx, cancel := context.WithCancel(context.Background())
	interactDone := make(chan interactResult, 1)
	opened := make(chan string, 1)
	go func() {
		ans, err := h.interact(ctx, "s1", Request{Kind: "question", Title: "问题"}, func(iid string) { opened <- iid })
		interactDone <- interactResult{ans: ans, err: err}
	}()
	var iid string
	select {
	case iid = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("交互没有登记")
	}
	respondDone := make(chan respondResult, 1)
	go func() {
		p, won, err := h.Respond(context.Background(), iid,
			store.Resolution{Outcome: OutcomeAnswered, Answer: "答案"}, Answer{Text: "答案"})
		respondDone <- respondResult{p: p, won: won, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Respond没有进入 ResolveInteraction")
	}
	cancel()
	var early *interactResult
	select {
	case got := <-interactDone:
		early = &got
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("Resolve阻塞期间取消应返回 context.Canceled，实际 ans=%+v err=%v", got.ans, got.err)
		}
	case <-time.After(time.Second):
		//修复实现会让取消分支等待 Respond的临界区；放行后再检查最终不变量。
	}
	close(release)
	var rr respondResult
	select {
	case rr = <-respondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Respond没有返回")
	}
	if rr.err != nil {
		t.Fatalf("Respond: %v", rr.err)
	}
	var ir interactResult
	if early != nil {
		ir = *early
	} else {
		select {
		case ir = <-interactDone:
		case <-time.After(5 * time.Second):
			t.Fatal("Interact没有返回")
		}
	}
	if rr.won {
		if ir.err != nil || ir.ans.Text != "答案" {
			t.Fatalf("Respond报告成功但回答丢失：interact=%+v respond=%+v", ir, rr)
		}
	} else if early != nil && rr.p.Outcome != OutcomeVoided {
		t.Fatalf("等待者已离开时 Respond应按 voided结束，实际 %+v", rr)
	}
}
