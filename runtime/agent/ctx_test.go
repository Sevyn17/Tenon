package agent

import (
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/store"
)

// 会话行（定义名与哈希、创建者、父会话、会话键、meta、使用配置）要落到 Ctx 上：
// 组件只看 Ctx 就能知道这次调用发生在哪个会话、谁触发、按哪份配置。
func TestBindCtx(t *testing.T) {
	a := &Agent{SessionID: "s1", Ctx: callctx.New(callctx.Options{Tenant: "local", SessionID: "s1"})}
	a.bindCtx(store.SessionMeta{
		ID: "s1", AgentID: "support", AgentHash: "deadbeef", Creator: "u42", ParentID: "p0",
		SessionKey: "ticket-1",
		Meta:       json.RawMessage(`{"shop":"s9"}`),
		UseConfig:  json.RawMessage(`{"memory":{"store":"memlib"}}`),
	})
	s := a.Ctx.Session()
	if s.Definition != "support" || s.Hash != "deadbeef" || s.Creator != "u42" || s.Parent != "p0" || s.Key != "ticket-1" {
		t.Fatalf("Session = %+v", s)
	}
	if m := a.Ctx.Meta(); m["shop"] != "s9" {
		t.Fatalf("Meta = %v", m)
	}
	if _, ok := a.Ctx.Use("memory"); !ok {
		t.Fatal("缺 memory 使用配置")
	}
	// 子会话标记不因会话行而被清掉。
	sub := &Agent{SessionID: "sub-1", Ctx: callctx.New(callctx.Options{SessionID: "sub-1", Child: true})}
	sub.bindCtx(store.SessionMeta{ID: "sub-1", ParentID: "s1"})
	if !sub.Ctx.Child() {
		t.Fatal("bindCtx 丢掉了 Child")
	}
	// 没配 Ctx 时按会话 id 兜底，工具不会拿到 nil。
	bare := &Agent{SessionID: "s2"}
	if k := bare.toolCtx(); k == nil || k.SessionID() != "s2" {
		t.Fatalf("toolCtx = %+v", bare.toolCtx())
	}
	// 也不会 panic。
	nilCtx := &Agent{SessionID: "s3"}
	nilCtx.bindCtx(store.SessionMeta{ID: "s3"})
}
