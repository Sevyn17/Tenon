package assemble

import (
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// 工具引用的权限项所属插件关掉时，装配期（Wire）就报错，不等到第一次构造会话（docs/62 §4.7、K6）。
func TestCheckItemRefs(t *testing.T) {
	tool := &toolhub.Tool{Items: []string{"shell.exec"}}
	tool.Spec.Name = "terminal"
	col := &Collected{Tools: []ToolSet{{Tools: func() []*toolhub.Tool { return []*toolhub.Tool{tool} }}}}
	err := CheckItemRefs(col)
	if err == nil || !strings.Contains(err.Error(), "terminal") || !strings.Contains(err.Error(), "shell.exec") ||
		!strings.Contains(err.Error(), "插件 shell") {
		t.Fatalf("应报未登记的权限项：%v", err)
	}
	col.Items = []toolhub.Item{{ID: "shell.exec"}}
	if err := CheckItemRefs(col); err != nil {
		t.Fatalf("已登记时不应报错：%v", err)
	}
}
