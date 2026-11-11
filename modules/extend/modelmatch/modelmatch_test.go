package modelmatch_test

// T5c：modelmatch 组件登记匹配器 llm；装配后 Assembly.Matcher("llm") 可取到。
// 且内核不内置 llm（只有 exact/prefix/list），证明「模型匹配器只能由组件登记」。

import (
	"testing"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/tenontest"
)

func TestLLMMatcherRegistered(t *testing.T) {
	app := tenontest.New(t, tenontest.Model(tenontest.Reply("ok")))
	asm := app.Internals().Assembly
	if _, ok := asm.Matcher("llm"); !ok {
		t.Fatal("装配后应有 llm 匹配器（由 modelmatch 组件登记）")
	}
	// 内核内置的纯程序匹配器仍在。
	for _, n := range []string{"exact", "prefix", "list"} {
		if _, ok := asm.Matcher(n); !ok {
			t.Fatalf("内置匹配器 %s 应在", n)
		}
	}
}

// 不装 modelmatch 时（Minimal 预设不含它）没有 llm 匹配器——证明模型匹配器不是内核内置。
func TestNoLLMMatcherWithoutComponent(t *testing.T) {
	app, err := tenon.New(tenon.Minimal(), tenon.WithStore(tenon.MemoryStore()), tenon.WithWorkdir(t.TempDir()),
		tenontest.Model())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(nil); err != nil {
		t.Fatal(err)
	}
	defer app.Close(nil)
	if _, ok := app.Internals().Assembly.Matcher("llm"); ok {
		t.Fatal("Minimal 预设不含 modelmatch，不应有 llm 匹配器")
	}
}
