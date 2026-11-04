package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ybase/runtime/toolhub"
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
