package checkpoint

// diff 引擎（spike 第九节）：在移植来的 godiff 上算行数、处理二进制与 CRLF。

import (
	"bytes"
	"strings"
)

// FileDiff 一个文件的 diff 结果。
type FileDiff struct {
	Path    string // 相对工作目录，正斜杠
	Binary  bool   // 二进制：Unified 为空，只给大小
	OldSize int64
	NewSize int64
	Added   int // 新增行数
	Removed int // 删除行数
	// Unified unified diff 文本（头部用 a/<path>、b/<path>；新建为 /dev/null → b/，删除为 a/ → /dev/null）。
	// 内容相同时为空。CRLF 原样保留在行里（不改字节）。
	Unified string
}

// Diff 算两份内容的 unified diff。oldData / newData 为 nil 表示该侧文件不存在。
func Diff(path string, oldData, newData []byte) FileDiff {
	d := FileDiff{Path: path, OldSize: int64(len(oldData)), NewSize: int64(len(newData))}
	if isBinary(oldData) || isBinary(newData) {
		d.Binary = true
		return d
	}
	if bytes.Equal(oldData, newData) && (oldData == nil) == (newData == nil) {
		return d
	}
	oldName, newName := "a/"+path, "b/"+path
	if oldData == nil {
		oldName = "/dev/null"
	}
	if newData == nil {
		newName = "/dev/null"
	}
	raw := godiff(oldName, oldData, newName, newData)
	if raw == nil { // 内容相同（如空文件新建）：只有头部
		d.Unified = "--- " + oldName + "\n+++ " + newName + "\n"
		return d
	}
	// 去掉 godiff 的首行 "diff a b"：unified diff 只要 ---/+++ 头。
	s := string(raw)
	if i := strings.IndexByte(s, '\n'); i >= 0 && strings.HasPrefix(s, "diff ") {
		s = s[i+1:]
	}
	d.Unified = s
	d.Added, d.Removed = countLines(s)
	return d
}

// countLines 数 unified diff 里的 +/- 行（不含 ---/+++ 头）。
func countLines(u string) (added, removed int) {
	for i, l := range strings.Split(u, "\n") {
		if i < 2 && (strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ")) {
			continue
		}
		switch {
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return
}
