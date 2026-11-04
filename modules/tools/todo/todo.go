package todo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ybase/runtime/toolhub"
)

// Todo 一条计划项。
type Todo struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

func checkTodos(todos []Todo) (p, i, c int, err error) {
	seen := map[string]bool{}
	for _, t := range todos {
		if strings.TrimSpace(t.Content) == "" {
			return 0, 0, 0, errors.New("invalid todo: `content` must be a non-empty string")
		}
		if seen[t.Content] {
			return 0, 0, 0, fmt.Errorf("invalid todos: duplicate content %q", t.Content)
		}
		seen[t.Content] = true
		switch t.Status {
		case "pending":
			p++
		case "in_progress":
			i++
		case "completed":
			c++
		default:
			return 0, 0, 0, fmt.Errorf("invalid todo: status %q（只能是 pending / in_progress / completed）", t.Status)
		}
	}
	return
}

// TodoWrite 整表替换的计划工具。列表的唯一事实是最后一次调用的参数（在日志里），onChange 只服务 UI。
func TodoWrite(onChange func([]Todo)) *toolhub.Tool {
	no := false
	t := toolhub.Simple("todo_write",
		"记录并更新任务计划。每次调用替换整个列表；status 为 pending / in_progress / completed。多步任务开始前先列计划，每完成一步立即更新。",
		`{"type":"object","properties":{"todos":{"type":"array","description":"完整列表（替换旧列表）","items":{"type":"object","properties":{"content":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["content","status"]}}},"required":["todos"]}`,
		func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Todos []Todo }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			p, i, c, err := checkTodos(a.Todos)
			if err != nil {
				return "", err
			}
			if onChange != nil {
				onChange(a.Todos)
			}
			return fmt.Sprintf("Updated todo list: %d pending, %d in progress, %d completed.", p, i, c), nil
		})
	t.Parallel = &no
	t.WithEffect(toolhub.EffectInteract)
	// 只改自己的计划列表，不碰外部资源：显式声明无需授权（§9.3）。
	t.NoAuth = true
	return t
}
