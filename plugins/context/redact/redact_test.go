package redact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ybase-kernel/core/callctx"
	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

func TestString(t *testing.T) {
	r := New("my-very-secret-value")
	cases := []struct{ in, want string }{
		{"key is my-very-secret-value.", "key is " + Placeholder + "."},
		{"Authorization: Bearer abcdefghijklmnop0123", "Authorization: Bearer " + Placeholder},
		{"sk-abcdefghijklmnopqrstuvwx", Placeholder},
		{"AKIAABCDEFGHIJKLMNOP", Placeholder},
		{"YBASE_API_KEY=abcd1234efgh", "YBASE_API_KEY=" + Placeholder},
		{`password: "hunter2hunter2"`, `password: "` + Placeholder + `"`},
		{"DATABASE_URL postgresql://bus:Pa55w0rd!x@localhost:5432/bus", "DATABASE_URL postgresql://bus:" + Placeholder + "@localhost:5432/bus"},
		{"redis://:s3cretpass@127.0.0.1:6379", "redis://:" + Placeholder + "@127.0.0.1:6379"},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----", Placeholder},
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
			return toolhub.Text("env: YBASE_API_KEY=sk-abcdefghijklmnopqrstuvwx"), nil
		},
	})
	h.AddLayer(Layer(New()))
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
