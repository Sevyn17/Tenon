package toolhub

import "testing"

// 内置的 read_artifact / tool_search 只读 hub 自己的东西，声明无需授权：原先没声明权限项，
// 权限组件按"未声明一律询问"处理，full 档位下翻一页超大结果也要人点（2026-11-08 v1 验收发现）。
func TestBuiltinToolsNoAuth(t *testing.T) {
	h := New(Options{})
	for _, tl := range []*Tool{h.readArtifactTool(), h.searchTool()} { // read_artifact 只在有工件存储时登记，直接取构造函数
		n := tl.Spec.Name
		if !tl.NoAuth || len(tl.Items) > 0 {
			t.Fatalf("%s 应声明 NoAuth 且不带权限项：NoAuth=%v Items=%v", n, tl.NoAuth, tl.Items)
		}
	}
}
