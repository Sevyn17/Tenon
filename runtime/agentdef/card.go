package agentdef

// Agent Card（T6a）：本地定义的公开部分，供远端发现。只导名字、描述、公开工具清单；
// 不导提示词、[use.*]（权限/预算）、caps、文件内容——避免泄漏提示词与权限（08 8.x）。

// Card 一份定义的公开名片。
type Card struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Tools       []string `json:"tools,omitempty"` // 定义声明的工具白名单（空 = 不限，不列）
}

// CardOf 从一份定义生成名片：只取公开字段。提示词、Use、Files 一律不导。
func CardOf(a Agent) Card {
	c := Card{ID: a.ID, Description: a.Description}
	if len(a.Tools) > 0 {
		c.Tools = append([]string(nil), a.Tools...)
	}
	return c
}
