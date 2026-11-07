package todo

import (
	"context"
	"encoding/json"
	"testing"

	"tenon/runtime/toolhub"
)

// 结果带结构化列表（Output.Data），界面不必解析文本（2026-11-04 审查 P2 工具体验）。
func TestTodoWriteData(t *testing.T) {
	tool := TodoWrite(nil)
	out, err := tool.Call(context.Background(), &toolhub.Cx{Tool: tool},
		json.RawMessage(`{"todos":[{"content":"a","status":"pending"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := out.Data.(map[string]any)
	ts, _ := m["todos"].([]Todo)
	if len(ts) != 1 || ts[0].Content != "a" {
		t.Fatalf("Data 应带列表：%#v", out.Data)
	}
}
