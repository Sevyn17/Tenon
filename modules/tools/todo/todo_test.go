package todo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/toolhub"
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

func TestTodoWrite(t *testing.T) {
	var got []Todo
	tool := TodoWrite(func(l []Todo) { got = l })
	s, _ := callTool(t, tool, `{"todos":[{"content":"a","status":"completed"},{"content":"b","status":"in_progress"},{"content":"c","status":"pending"}]}`)
	if s != "Updated todo list: 1 pending, 1 in progress, 1 completed." || len(got) != 3 {
		t.Fatalf("%q %v", s, got)
	}
	for _, bad := range []string{
		`{"todos":[{"content":" ","status":"pending"}]}`,
		`{"todos":[{"content":"a","status":"pending"},{"content":"a","status":"completed"}]}`,
		`{"todos":[{"content":"a","status":"done"}]}`,
	} {
		got = nil
		s, _ := callTool(t, tool, bad)
		if !strings.Contains(s, "invalid") || got != nil {
			t.Fatalf("%s → %q", bad, s)
		}
	}
	if tool.Parallel == nil || *tool.Parallel {
		t.Fatal("todo_write 不应并行")
	}
}
