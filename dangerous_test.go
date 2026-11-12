package tenon

// M8 回归：Tool.Dangerous() 的文档承诺「权限插件按档位要人确认（缺省档位下也要）」。
//
// 原来它只设 Risk = Dangerous，而权限组件**完全不读 Risk**（读 AttrDestructive），
// 所以 Dangerous 与 Writes 行为完全相同——文档承诺是假的。
// 修法：Dangerous 同时设 attrDestructive。这个用例锁住「属性真的设上了」。

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func newTestTool(name string) Tool {
	return Func(name, "测试", func(ctx context.Context, args struct{}) (any, error) { return "", nil })
}

// Dangerous 设了 destructive 属性（权限组件据此升到 AskUser）。
func TestDangerousSetsDestructiveAttr(t *testing.T) {
	tool := newTestTool("rm").Dangerous()
	if tool.t.Risk != toolhub.Dangerous {
		t.Fatalf("Risk 应为 Dangerous，实际 %v", tool.t.Risk)
	}
	if on, _ := tool.t.Attrs[attrDestructive].(bool); !on {
		t.Fatalf("Dangerous 应设 %s 属性（否则权限组件不读它，与 Writes 等价）：%+v",
			attrDestructive, tool.t.Attrs)
	}
	if tool.t.NoAuth {
		t.Fatal("Dangerous 不应标 NoAuth")
	}
}

// 对照：Writes 不设 destructive（它按档位判，不需要强制确认）。
func TestWritesDoesNotSetDestructive(t *testing.T) {
	tool := newTestTool("w").Writes()
	if on, _ := tool.t.Attrs[attrDestructive].(bool); on {
		t.Fatal("Writes 不该设 destructive（那会让所有写操作强制确认）")
	}
}

// Dangerous 与 Writes 现在行为不同（原来等价）。
func TestDangerousDiffersFromWrites(t *testing.T) {
	d := newTestTool("a").Dangerous()
	w := newTestTool("b").Writes()
	dAttr, _ := d.t.Attrs[attrDestructive].(bool)
	wAttr, _ := w.t.Attrs[attrDestructive].(bool)
	if dAttr == wAttr {
		t.Fatal("Dangerous 与 Writes 应行为不同（destructive 属性只在 Dangerous 上）")
	}
}
