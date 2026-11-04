package mcp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 超长描述按 UTF-8 边界截断（2026-11-04 审查 P2：原先按字节切，可能切坏多字节字符）。
func TestDescClipUTF8(t *testing.T) {
	c := &Client{Server: Server{Name: "s"}}
	ts := c.Tools([]RemoteTool{{Name: "x", Description: strings.Repeat("中", DescMax)}})
	if d := ts[0].Spec.Description; !utf8.ValidString(d) {
		t.Fatalf("描述不是合法 UTF-8")
	}
}
