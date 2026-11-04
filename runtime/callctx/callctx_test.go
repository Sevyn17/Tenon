package callctx

import (
	"encoding/json"
	"testing"
)

// 会话行填进 Ctx 之后，组件只看 Ctx 就能拿到租户、会话、使用配置与业务 meta。
func TestBindAndRead(t *testing.T) {
	c := New(Options{Tenant: "local", Workdir: "/w", SessionID: "s1"})
	if got := c.Tenant(); got != "local" {
		t.Fatalf("Tenant = %q", got)
	}
	if got := c.SessionID(); got != "s1" {
		t.Fatalf("SessionID = %q", got)
	}
	if got := c.Workdir(); got != "/w" {
		t.Fatalf("Workdir = %q", got)
	}
	if _, ok := c.Use("memory"); ok {
		t.Fatal("未绑定时不该有使用配置")
	}

	c.Bind(Session{ID: "s1", Definition: "support", Hash: "abc", Creator: "u42", Parent: "p0", Key: "ticket-1"},
		json.RawMessage(`{"user":"u42","shop":"s9"}`), json.RawMessage(`{"memory":{"store":"memlib"}}`))

	s := c.Session()
	if s.Definition != "support" || s.Hash != "abc" || s.Creator != "u42" || s.Parent != "p0" || s.Key != "ticket-1" {
		t.Fatalf("Session = %+v", s)
	}
	if m := c.Meta(); m["user"] != "u42" || m["shop"] != "s9" {
		t.Fatalf("Meta = %v", m)
	}
	use, ok := c.Use("memory")
	if !ok {
		t.Fatal("缺 memory 使用配置")
	}
	if m, _ := use.(map[string]any); m["store"] != "memlib" {
		t.Fatalf("Use(memory) = %v", use)
	}
	// Meta 返回副本：调用方改了不影响内核。
	c.Meta()["user"] = "hacked"
	if c.Meta()["user"] != "u42" {
		t.Fatal("Meta 不是副本")
	}
}

// 子会话标记由构造方决定，Bind 不会把它覆盖掉（存储里没有这一列）。
func TestBindKeepsChild(t *testing.T) {
	c := New(Options{SessionID: "sub-1", Child: true})
	c.Bind(Session{ID: "sub-1"}, nil, nil)
	if !c.Child() {
		t.Fatal("Bind 丢掉了 Child")
	}
}

// 零值安全：组件拿不到上下文（装配期、老路径）时读到的都是空值，不该 panic。
func TestNilSafe(t *testing.T) {
	var c *Ctx
	if c.Tenant() != "" || c.SessionID() != "" || c.Child() || c.Workdir() != "" || c.Meta() != nil {
		t.Fatal("nil Ctx 应读到空值")
	}
	if _, ok := c.Use("x"); ok {
		t.Fatal("nil Ctx 不该有使用配置")
	}
	c.Bind(Session{ID: "x"}, nil, nil) // 不该 panic
}

// 坏 JSON 按空对象处理：meta 是应用写的数据，坏值不该让调用失败。
func TestBadJSON(t *testing.T) {
	c := New(Options{SessionID: "s"})
	c.Bind(Session{ID: "s"}, json.RawMessage(`{"user":`), json.RawMessage(`[1,2]`))
	if c.Meta() != nil {
		t.Fatalf("坏 meta 应得 nil，实际 %v", c.Meta())
	}
	if _, ok := c.Use("x"); ok {
		t.Fatal("非对象的 use 应得空")
	}
}

func TestTrace(t *testing.T) {
	c := New(Options{SessionID: "s"})
	if c.Trace().ID != "" {
		t.Fatal("未填时 trace 为空")
	}
	c.BindTrace(Trace{ID: "t-1"})
	if c.Trace().ID != "t-1" {
		t.Fatalf("Trace = %+v", c.Trace())
	}
}
