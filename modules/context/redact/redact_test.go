package redact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func TestString(t *testing.T) {
	r := New("my-very-secret-value")
	cases := []struct{ in, want string }{
		{"key is my-very-secret-value.", "key is " + Placeholder + "."},
		{"Authorization: Bearer abcdefghijklmnop0123", "Authorization: Bearer " + Placeholder},
		{"sk-abcdefghijklmnopqrstuvwx", Placeholder},
		{"AKIAABCDEFGHIJKLMNOP", Placeholder},
		{"TENON_API_KEY=abcd1234efgh", "TENON_API_KEY=" + Placeholder},
		{`password: "hunter2hunter2"`, `password: "` + Placeholder + `"`},
		{"DATABASE_URL postgresql://bus:Pa55w0rd!x@localhost:5432/bus", "DATABASE_URL postgresql://bus:" + Placeholder + "@localhost:5432/bus"},
		{"redis://:s3cretpass@127.0.0.1:6379", "redis://:" + Placeholder + "@127.0.0.1:6379"},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----", Placeholder},
		// JSON / YAML 常见形状（审查 P1-11：原先键名带引号、引号值带空格都漏）。
		{`{"api_key": "abcd1234efgh5678"}`, `{"api_key": "` + Placeholder + `"}`},
		{`"x-api-key": "zzzzzzzzzzzzzzzz"`, `"x-api-key": "` + Placeholder + `"`},
		{`password: "correct horse battery"`, `password: "` + Placeholder + `"`},
		{`'db_password': 'p4ss w0rd!!'`, `'db_password': '` + Placeholder + `'`},
	}
	for _, c := range cases {
		if got, n := r.String(c.in); got != c.want || n == 0 {
			t.Errorf("%q → %q (n=%d)，期望 %q", c.in, got, n, c.want)
		}
	}
	for _, s := range []string{"Bearer of good news", "token count: 12", "hello world", "skip-this", "https://example.com/a@b", "git@github.com:x/y.git", "ssh://git@host/repo"} {
		if got, n := r.String(s); got != s || n != 0 {
			t.Errorf("误伤 %q → %q", s, got)
		}
	}
}

func TestFromEnv(t *testing.T) {
	r := FromEnv([]string{"MY_TOKEN=zzzzzzzzzz", "PATH=/usr/bin:/bin/abcdefgh", "SHORT_KEY=abc"})
	if got, _ := r.String("x zzzzzzzzzz y"); got != "x "+Placeholder+" y" {
		t.Fatal(got)
	}
	if got, _ := r.String("/usr/bin:/bin/abcdefgh abc"); got != "/usr/bin:/bin/abcdefgh abc" {
		t.Fatal("非密钥变量不应替换：", got)
	}
	// 名字像密钥、值却是路径的变量不收（审查 P1-12：否则该路径处处变占位符，WriteGuard 再拒写）。
	r = FromEnv([]string{`SSH_KEY_DIR=C:\Users\me\.ssh`, "KEYCHAIN_PATH=/Users/me/Library/kc",
		"GPG_KEYFILE=secring.gpg", `MONKEY=D:\work\repo`, "API_KEY=abcd1234efgh"})
	for _, s := range []string{`C:\Users\me\.ssh`, "/Users/me/Library/kc", "secring.gpg", `D:\work\repo`} {
		if got, _ := r.String(s); got != s {
			t.Errorf("路径值不应当成密钥：%q → %q", s, got)
		}
	}
	if got, _ := r.String("k=abcd1234efgh"); got != "k="+Placeholder {
		t.Fatal("真正的密钥仍应替换：", got)
	}
}

func TestContentKeepsNonText(t *testing.T) {
	r := New("secretsecret")
	in := session.Content{session.NewTextPart("a secretsecret"), {Kind: "image", Text: "secretsecret"}}
	out, n := r.Content(in)
	if n != 1 || out[0].Text != "a "+Placeholder || out[1].Text != "secretsecret" {
		t.Fatalf("%+v n=%d", out, n)
	}
	if in[0].Text != "a secretsecret" {
		t.Fatal("不应修改原内容")
	}
}

func TestLayerPersistsRedacted(t *testing.T) {
	h := toolhub.New(toolhub.Options{})
	h.Register(&toolhub.Tool{
		Spec: session.ToolSpec{Name: "leak", Schema: json.RawMessage(`{"type":"object"}`)},
		Risk: toolhub.Read,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage) (toolhub.Output, error) {
			return toolhub.Text("env: TENON_API_KEY=sk-abcdefghijklmnopqrstuvwx"), nil
		},
	})
	// 04 4.3.1：脱敏从「会话层」迁到 ToolCall 中间件；测试里把它包成一层挂上去。
	h.AddLayer(toolhub.Layer{Name: "redact", Call: mwAsLayer(Middleware(New()))})
	res := h.Execute(context.Background(), kctx("s"), []session.ToolCall{{ID: "c1", Name: "leak", Args: json.RawMessage(`{}`)}}, nil)
	r0, ok := res.Next()
	if !ok || r0.Err != nil {
		t.Fatal(r0.Err)
	}
	txt := r0.Output.Content[0].Text
	if strings.Contains(txt, "sk-abc") || !strings.Contains(txt, Placeholder) {
		t.Fatal(txt)
	}
}

// kctx 测试用调用上下文（框架设计 04 §4.4）。
func kctx(id string) *callctx.Ctx { return callctx.New(callctx.Options{SessionID: id}) }

// mwAsLayer 把 ToolCall 中间件包成 toolhub.Layer（测试用；生产路径由 assemble 装配）。
func mwAsLayer(mw component.ToolCallMw) func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
	return func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
		req := component.ToolCallReq{Args: args}
		if cx != nil && cx.Tool != nil {
			req.Tool = cx.Tool.Spec.Name
		}
		if cx != nil {
			req.SessionID = cx.SessionID
		}
		resp, err := mw(ctx, req, func(ctx context.Context, r component.ToolCallReq) (component.ToolCallResp, error) {
			out, err := next(ctx, cx, r.Args)
			return component.ToolCallResp{Output: out}, err
		})
		if err != nil {
			return toolhub.Output{}, err
		}
		if resp.Denied {
			return toolhub.Failf("%s", resp.Reason), nil
		}
		return resp.Output, nil
	}
}
