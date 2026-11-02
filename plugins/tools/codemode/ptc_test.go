package codemode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ybase-kernel/core/callctx"
	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

func setup(t *testing.T) (*toolhub.Hub, *toolhub.Toolset, *[]string) {
	t.Helper()
	hub := toolhub.New(toolhub.Options{})
	var seen []string
	echo := &toolhub.Tool{
		Spec: session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
		Call: func(_ context.Context, cx *toolhub.Cx, args json.RawMessage) (toolhub.Output, error) {
			seen = append(seen, cx.CallID)
			var a struct{ Text string }
			_ = json.Unmarshal(args, &a)
			if a.Text == "bad" {
				return toolhub.Errorf("坏输入"), nil
			}
			return toolhub.Text("echo:" + a.Text), nil
		},
	}
	for _, tl := range []*toolhub.Tool{echo, Tool()} {
		if err := hub.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	return hub, hub.SpecsFor(nil, 0), &seen
}

func call(t *testing.T, hub *toolhub.Hub, ts *toolhub.Toolset, code string) toolhub.Output {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"code": code, "timeout": 2})
	run := hub.ExecuteIn(context.Background(), ts, kctx("s1"), []session.ToolCall{{ID: "c1", Name: Name, Args: args}}, nil)
	res, _ := run.Next()
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	return res.Output
}

func text(o toolhub.Output) string { return contentText(o.Content) }

func TestSubCalls(t *testing.T) {
	hub, ts, seen := setup(t)
	out := call(t, hub, ts, `
		const r = [];
		for (const w of ["a","b","c"]) r.push(tools.echo({text: w}));
		console.log("n=" + r.length);
		r.join(",")`)
	if out.Status != session.ToolOK || text(out) != "n=3\necho:a,echo:b,echo:c" {
		t.Fatalf("got %v %q", out.Status, text(out))
	}
	if strings.Join(*seen, " ") != "c1#1 c1#2 c1#3" {
		t.Fatalf("ids %v", *seen)
	}
}

func TestToolErrorThrows(t *testing.T) {
	hub, ts, _ := setup(t)
	out := call(t, hub, ts, `try { tools.echo({text:"bad"}) } catch (e) { "caught:" + e.message }`)
	if text(out) != "caught:坏输入" {
		t.Fatalf("got %q", text(out))
	}
	out = call(t, hub, ts, `tools.echo({text:"bad"})`)
	if out.Status != session.ToolError || !strings.Contains(text(out), "[exception]") {
		t.Fatalf("got %v %q", out.Status, text(out))
	}
}

func TestNoSelfAndNoGlobals(t *testing.T) {
	hub, ts, _ := setup(t)
	out := call(t, hub, ts, `[typeof tools.run_code, typeof require, typeof setTimeout].join(",")`)
	if text(out) != "undefined,undefined,undefined" {
		t.Fatalf("got %q", text(out))
	}
}

func TestTimeout(t *testing.T) {
	hub, ts, _ := setup(t)
	start := time.Now()
	out := call(t, hub, ts, `while (true) {}`)
	if !strings.Contains(text(out), "[timeout]") || time.Since(start) > 5*time.Second {
		t.Fatalf("got %q after %v", text(out), time.Since(start))
	}
}

func TestOutputLimit(t *testing.T) {
	hub, ts, _ := setup(t)
	out := call(t, hub, ts, `for (;;) console.log("x".repeat(1000))`)
	if !strings.Contains(text(out), "[output-limit]") {
		t.Fatalf("got %q", text(out)[:100])
	}
}

func TestObjectResult(t *testing.T) {
	hub, ts, _ := setup(t)
	out := call(t, hub, ts, `({a: 1, b: [2]})`)
	if text(out) != `{"a":1,"b":[2]}` {
		t.Fatalf("got %q", text(out))
	}
}

// kctx 测试用调用上下文（框架设计 04 §4.4）。
func kctx(id string) *callctx.Ctx { return callctx.New(callctx.Options{SessionID: id}) }
