package redact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func TestAssignSkipsCode(t *testing.T) {
	r := New()
	keep := []string{
		"const token = createToken(user)",
		"password: process.env.DB_PASSWORD",
		"apiKey = config.apiKey",
		"token = refreshToken",
	}
	for _, s := range keep {
		if got, n := r.String(s); n != 0 {
			t.Errorf("误伤代码 %q → %q", s, got)
		}
	}
	hit := []string{
		`password = "hunter2hunter2"`,
		"API_KEY=ab12cd34ef56gh78",
		"token: 9f8e7d6c5b4a3210",
	}
	for _, s := range hit {
		if got, n := r.String(s); n == 0 || !strings.Contains(got, Placeholder) {
			t.Errorf("漏脱敏 %q → %q", s, got)
		}
	}
}

func TestWriteGuard(t *testing.T) {
	g := WriteGuard()
	ran := false
	next := func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
		ran = true
		return toolhub.Text("ok"), nil
	}
	w := &toolhub.Tool{Attrs: map[string]any{toolhub.AttrWorkspace: true}}
	out, _ := g.Call(context.Background(), &toolhub.Cx{Tool: w}, json.RawMessage(`{"content":"x=`+Placeholder+`"}`), next)
	if ran || out.Status != session.ToolError {
		t.Fatalf("应拒绝写回占位符：ran=%v out=%+v", ran, out)
	}
	if _, _ = g.Call(context.Background(), &toolhub.Cx{Tool: w}, json.RawMessage(`{"content":"x=1"}`), next); !ran {
		t.Fatal("无占位符应放行")
	}
	ran = false
	r := &toolhub.Tool{}
	g.Call(context.Background(), &toolhub.Cx{Tool: r}, json.RawMessage(`{"q":"`+Placeholder+`"}`), next)
	if !ran {
		t.Fatal("非写文件工具应放行")
	}
}
