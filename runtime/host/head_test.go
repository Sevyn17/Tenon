package host

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/store"
)

// fullLoadCounter 数整份日志的 Load（base == nil：要读并反序列化全部条目）。
type fullLoadCounter struct {
	Store
	full atomic.Int32
}

func (c *fullLoadCounter) Load(ctx context.Context, id string, base *int) (*store.Loaded, error) {
	if base == nil {
		c.full.Add(1)
	}
	return c.Store.Load(ctx, id, base)
}

// 投递只读会话行，不整份加载日志（2026-11-04 审查 P1-34：原先每封信 Exists + SessionStatus + 整份 Load，
// 成本随日志长度增长）。
func TestDeliverReadsHeadOnly(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	// 打开会话之前就包上计数器：回合结束会在后台派发发件箱、读 h.DB，回合后再换字段是数据竞争。
	c := &fullLoadCounter{Store: r.db}
	r.h.DB = c
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, Text: "一"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	c.full.Store(0) // 只数下面投递时的整份加载
	if err := r.h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrComponent, ID: "test"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Body: "二"}); err != nil {
		t.Fatal(err)
	}
	if n := c.full.Load(); n != 0 {
		t.Fatalf("投递时整份加载日志 %d 次，期望 0", n)
	}
	if err := r.h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrComponent, ID: "test"},
		To: mail.Address{Kind: mail.AddrSession, ID: "nope"}, Body: "x"}); err == nil {
		t.Fatal("不存在的会话应报错")
	}
	_ = hd.Wait()
}
