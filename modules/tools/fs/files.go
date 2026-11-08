package fs

// write_file / list_dir 的 toolhub 版本（替换旧 Builtin 集里的同名工具）。
// 写入用 writeAtomic（临时文件 + 改名），路径限制在工作目录内。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// ListDirMax list_dir 最多列出的条目数。
const ListDirMax = 1000

// WriteFile 整文件写入（新建或覆盖，自动建父目录）。修改已有文件应优先用 edit。
// tr 可选：覆盖已存在的文件前做读后写检查（审查 P1-43）。
func WriteFile(root string, tr ...*Tracker) *toolhub.Tool {
	root, _ = filepath.Abs(root)
	track := firstTracker(tr)
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: "write_file",
			Description: "写入文本文件（新建或整体覆盖，自动创建父目录；路径相对工作目录）。修改已有文件请用 edit。",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`)},
		Risk:  toolhub.Write,
		Attrs: map[string]any{toolhub.AttrWorkspace: true},
		Call: func(_ context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct{ Path, Content string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return toolhub.Failf("参数不合法：%v", err), nil
			}
			p, err := resolve(root, a.Path)
			if err != nil {
				return toolhub.Failf("%v", err), nil
			}
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				return toolhub.Failf("%s 是目录", a.Path), nil
			}
			if err := track.Check(cx, p, a.Path); err != nil {
				return toolhub.Failf("%v", err), nil
			}
			if err := writeAtomic(p, []byte(a.Content)); err != nil {
				return toolhub.Failf("写入失败：%v", err), nil
			}
			track.Note(cx, p)
			return toolhub.Text(fmt.Sprintf("已写入 %s（%d 字节）", a.Path, len(a.Content))), nil
		},
	}
}

// ListDir 列出目录（目录名带 /，按名排序；默认工作目录）。
func ListDir(root string) *toolhub.Tool {
	root, _ = filepath.Abs(root)
	return toolhub.Simple("list_dir", "列出目录内容（路径相对工作目录，默认工作目录；目录名以 / 结尾）。递归查找文件用 glob。",
		`{"type":"object","properties":{"path":{"type":"string"}}}`,
		func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Path string }
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", fmt.Errorf("参数不合法：%v", err)
				}
			}
			p, err := resolve(root, a.Path)
			if err != nil {
				return "", err
			}
			ents, err := os.ReadDir(p)
			if err != nil {
				return "", err
			}
			names := make([]string, 0, len(ents))
			for _, e := range ents {
				n := e.Name()
				if e.IsDir() {
					n += "/"
				}
				names = append(names, n)
			}
			sort.Strings(names)
			more := ""
			if len(names) > ListDirMax {
				more = fmt.Sprintf("\n…（共 %d 项，只列出前 %d 项）", len(names), ListDirMax)
				names = names[:ListDirMax]
			}
			if len(names) == 0 {
				return "（空目录）", nil
			}
			return strings.Join(names, "\n") + more, nil
		})
}
