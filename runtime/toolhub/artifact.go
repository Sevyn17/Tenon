package toolhub

// 超大结果存工件（tool-hub D3 / D7）：结果超过阈值（字节或行数，先到者）时，全文存工件，
// 只回预览 + 引用；模型用内置 read_artifact 按行翻页。预览方向由工具 Attrs["preview"] 声明。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ybase/runtime/session"
)

// ArtifactPrefix 工件引用前缀。
const ArtifactPrefix = "artifact://"

// spill 超阈值时存工件；返回替换后的输出与工件哈希（未存为空）。
func (h *Hub) spill(ctx context.Context, sessionID string, t *Tool, out Output) (Output, string) {
	if h.art == nil {
		return out, ""
	}
	text := contentText(out.Content)
	lines := strings.Count(text, "\n") + 1
	if len(text) <= h.thresholds.Bytes && lines <= h.thresholds.Lines {
		return out, ""
	}
	hash, err := h.art.Put(ctx, sessionID, "text/plain", []byte(text))
	if err != nil {
		return out, "" // 存不了就保留原结果（内核 MaxResultBytes 兜底）
	}
	mode, _ := t.Attrs[AttrPreview].(string)
	pv := Preview(text, mode, h.thresholds.Preview)
	note := fmt.Sprintf("\n…（结果过大：共 %d 字节、%d 行，已存为 %s%s ，用 read_artifact 按行翻页，每页 %d 行）",
		len(text), lines, ArtifactPrefix, hash, h.thresholds.PageLines)
	out.Content = session.Content{session.NewTextPart(pv + note)}
	return out, hash
}

// Preview 按方向截取预览：head（默认）/ tail / head_tail（头尾各一半，中间注明省略量）。按 rune 边界截断。
func Preview(s, mode string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(string(r)) <= n {
		return s
	}
	take := func(rs []rune, max int, fromEnd bool) string {
		out, size := []rune{}, 0
		if fromEnd {
			for i := len(rs) - 1; i >= 0; i-- {
				size += len(string(rs[i]))
				if size > max {
					break
				}
				out = append([]rune{rs[i]}, out...)
			}
			return string(out)
		}
		for _, c := range rs {
			size += len(string(c))
			if size > max {
				break
			}
			out = append(out, c)
		}
		return string(out)
	}
	switch mode {
	case "tail":
		return "…（前略）\n" + take(r, n, true)
	case "head_tail":
		head, tail := take(r, n/2, false), take(r, n/2, true)
		omitted := len(s) - len(head) - len(tail)
		return fmt.Sprintf("%s\n…（中间省略 %d 字节）…\n%s", head, omitted, tail)
	default:
		return take(r, n, false)
	}
}

// readArtifactTool 内置 read_artifact：按行翻页。
func (h *Hub) readArtifactTool() *Tool {
	schema := `{"type":"object","properties":{"ref":{"type":"string","description":"artifact://<sha256>"},` +
		`"offset":{"type":"integer","description":"起始行（从 1 开始）"},"limit":{"type":"integer"}},"required":["ref"]}`
	return &Tool{
		Spec: session.ToolSpec{Name: "read_artifact", Description: "按行读取已存为工件的超大工具结果", Schema: json.RawMessage(schema)},
		Risk: Read,
		Call: func(_ context.Context, _ *Cx, raw json.RawMessage) (Output, error) {
			var a struct {
				Ref           string
				Offset, Limit int
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return Failf("参数：%v", err), nil
			}
			hash := strings.TrimPrefix(a.Ref, ArtifactPrefix)
			b, err := h.art.Get(hash)
			if err != nil {
				return Failf("工件 %s 不可读：%v", a.Ref, err), nil
			}
			ls := strings.Split(string(b), "\n")
			if a.Offset < 1 {
				a.Offset = 1
			}
			if a.Limit <= 0 {
				a.Limit = h.thresholds.PageLines
			}
			if a.Offset > len(ls) {
				return Failf("offset %d 超出总行数 %d", a.Offset, len(ls)), nil
			}
			end := a.Offset - 1 + a.Limit
			if end > len(ls) {
				end = len(ls)
			}
			body := strings.Join(ls[a.Offset-1:end], "\n")
			if end < len(ls) {
				body += fmt.Sprintf("\n…（第 %d–%d 行，共 %d 行；继续用 offset=%d）", a.Offset, end, len(ls), end+1)
			}
			return Text(body), nil
		},
	}
}
