package toolhub

import (
	"encoding/json"
	"testing"
)

// 恢复后参数是日志里压缩过的形式；哈希必须与原始（带空白）形式一致，否则批准记录对不上（M4.9 验收暴露）。
func TestArgsHashCanonical(t *testing.T) {
	a := ArgsHash(json.RawMessage(`{"path": "b.txt", "content": "olleh"}`))
	b := ArgsHash(json.RawMessage(`{"path":"b.txt","content":"olleh"}`))
	if a != b {
		t.Fatal("空白不同的同一参数哈希应相同")
	}
	if a == ArgsHash(json.RawMessage(`{"path":"c.txt","content":"olleh"}`)) {
		t.Fatal("不同参数哈希应不同")
	}
}

// 含 < > & 的参数：模型原文与日志恢复后的转义形式哈希必须一致。
func TestArgsHashHTMLEscape(t *testing.T) {
	raw := ArgsHash(json.RawMessage(`{"command":"npx jest 2>&1 | x"}`))
	esc := ArgsHash(json.RawMessage(`{"command":"npx jest 2\u003e\u00261 | x"}`))
	if raw != esc {
		t.Fatal("恢复前后哈希不一致")
	}
}
