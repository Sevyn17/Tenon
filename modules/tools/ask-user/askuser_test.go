package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"tenon/runtime/session"
	"tenon/runtime/toolhub"
)

func callTool(t *testing.T, tool *toolhub.Tool, args string) (string, bool) {
	t.Helper()
	out, err := tool.Call(context.Background(), &toolhub.Cx{Tool: tool}, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, p := range out.Content {
		sb.WriteString(p.Text)
	}
	return sb.String(), out.Status.State == "ok"
}

func TestAskUser(t *testing.T) {
	s, _ := callTool(t, AskUser(nil), `{"question":"用哪个？"}`)
	if !strings.Contains(s, "无人可问") {
		t.Fatal(s)
	}
	var q string
	var opts []string
	tool := AskUser(func(_ context.Context, question string, o []string) (string, error) {
		q, opts = question, o
		return "B", nil
	})
	s, _ = callTool(t, tool, `{"question":"用哪个？","options":["A","B"]}`)
	if s != `{"answer":"B"}` || q != "用哪个？" || len(opts) != 2 {
		t.Fatalf("%q %q %v", s, q, opts)
	}
	s, _ = callTool(t, AskUser(func(context.Context, string, []string) (string, error) {
		return "", errors.New("取消")
	}), `{"question":"x"}`)
	if !strings.Contains(s, "取消") {
		t.Fatal(s)
	}
	s, _ = callTool(t, tool, `{"question":""}`)
	if !strings.Contains(s, "不能为空") {
		t.Fatal(s)
	}
}

func TestAskChoice(t *testing.T) {
	var gotKind string
	var gotBody map[string]any
	tool := AskChoice(func(_ context.Context, sid, kind, title string, body map[string]any) (json.RawMessage, error) {
		gotKind, gotBody = kind, body
		return json.RawMessage(`"b"`), nil
	})
	call := func(args string) (string, bool) {
		out, err := tool.Call(context.Background(), &toolhub.Cx{SessionID: "s"}, json.RawMessage(args))
		if err != nil {
			t.Fatal(err)
		}
		txt := ""
		for _, p := range out.Content {
			txt += p.Text
		}
		return txt, out.Status == session.ToolError
	}
	out, isErr := call(`{"question":"选哪个","options":[{"id":"a","label":"A"},{"id":"b","label":"B","description":"更快"}]}`)
	if isErr || out != `{"selected":"b"}` || gotKind != "choice" || gotBody["text"] != "选哪个" {
		t.Fatalf("%q %v %s %+v", out, isErr, gotKind, gotBody)
	}
	for _, bad := range []string{`{"question":"q","options":[{"id":"a","label":"A"}]}`, `{"question":"q","options":[{"id":"a","label":"A"},{"id":"a","label":"B"}]}`, `{"question":" ","options":[]}`} {
		if _, isErr := call(bad); !isErr {
			t.Errorf("%s 应失败", bad)
		}
	}
}
