package instructions

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
)

// 嵌套指令的"已装载"按会话历史推导（2026-11-04 审查 P1-26：原先只在内存里记，
// 会话恢复后重复追加，压缩掉之后不再追加，模型在该目录里就丢了这些指令）。
func TestNestedInstructionsFollowHistory(t *testing.T) {
	root := t.TempDir()
	put(t, filepath.Join(root, "pkg", "AGENTS.md"), "包规则")
	put(t, filepath.Join(root, "pkg", "a.txt"), "hi")

	// 会话恢复：历史里已经有这条嵌套指令 → 不再追加。
	prior := "hi\n\n" + nestedMark + "\n该目录有新的项目指令，后续在此目录工作时遵守：\n\n# 来自 pkg/AGENTS.md\n\n包规则\n</project-instructions>"
	window := []*session.Entry{{Item: session.Item{Kind: session.KindToolResult,
		ToolResult: &session.ToolResultItem{CallID: "c0", Output: session.Content{session.NewTextPart(prior)}}}}}
	base := 0
	in := &Instructions{Workdir: root, History: func() (int, []*session.Entry) { return base, window }}
	if d := in.Discover("pkg/a.txt"); d != "" {
		t.Fatalf("恢复后历史里已有的嵌套指令不应重复追加：%q", d)
	}

	// 压缩：那条工具结果被摘要吃掉（压缩点前移、窗口里没有了）→ 再进入该目录时重新追加。
	base, window = 5, nil
	if d := in.Discover("pkg/a.txt"); !strings.Contains(d, "包规则") {
		t.Fatalf("压缩掉之后应重新追加嵌套指令：%q", d)
	}
	// 追加过一次后同一窗口内不再重复。
	if d := in.Discover("pkg/a.txt"); d != "" {
		t.Fatalf("同一压缩窗口内只追加一次：%q", d)
	}
}
