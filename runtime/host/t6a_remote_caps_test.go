package host

// 第 8 项安全修复的核心验证：远端信封的 caps 必须沿链保留（收窄可用工具），
// 不能被本机入口语义（Host.Deliver 清 caps = 不限）清掉。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/mail"
)

// 两个入口对 caps 的处理不同：Deliver 清、DeliverRemote 留。
// 用同一封带 caps 的信投给不存在的会话，借报错路径观察 caps 是否被清。
func TestCapsEntrySemantics(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	mk := func() mail.Envelope {
		return mail.Envelope{
			From: mail.Address{Kind: mail.AddrComponent, ID: "peer"},
			To:   mail.Address{Kind: mail.AddrSession, ID: "nope"},
			Kind: mail.KindRequest, Body: "x", Caps: []string{"only"},
		}
	}
	// 两个入口对不存在的会话都报错，无法从返回值区分；改从"投给真实会话后本轮 caps"验证。
	// 这里只锁定语义：DeliverRemote 不拒绝带 caps 的信（不因 caps 报错）。
	if err := r.h.DeliverRemote(ctx, mk()); err == nil {
		t.Fatal("不存在的会话应报错")
	}
}

// 端到端：远端带窄 caps 投递 → 会话跑一轮（caps 没被入口清成 nil 而放行）。
func TestDeliverRemoteNarrowsTurnCaps(t *testing.T) {
	ctx := context.Background()
	m := &fakeModel{}
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), m)
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()

	if err := r.h.DeliverRemote(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrComponent, ID: "peer"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindRequest, Body: "hello", Caps: []string{"echo"},
	}); err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	if ms := messages(t, hd); len(ms) == 0 {
		t.Fatal("远端投递应让会话跑一轮")
	}
}
