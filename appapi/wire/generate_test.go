package wire

import (
	"strings"
	"testing"
)

type genDemo struct {
	Text  string `json:"text"`
	Parts []Part `json:"parts,omitempty"`
	Empty *Empty `json:"empty,omitempty"`
}

// Generate：产品自己的类型生成 TS，引用基础类型时 import type，不内联。
func TestGenerate(t *testing.T) {
	ts := Generate([]any{genDemo{}}, map[string]any{"DemoKinds": []string{"a", "b"}},
		GenOptions{Header: "demo", Import: "@tenon/client", Base: []any{Part{}, Empty{}, Entry{}}})
	for _, want := range []string{
		"// demo\n",
		`import type { Empty, Part } from "@tenon/client";`,
		`export const DemoKinds = ["a", "b"] as const;`,
		"export type DemoKind = (typeof DemoKinds)[number];",
		"export interface genDemo {\n  text: string;\n  parts?: Part[];\n  empty?: Empty;\n}",
	} {
		if !strings.Contains(ts, want) {
			t.Errorf("缺 %q\n%s", want, ts)
		}
	}
	if strings.Contains(ts, "Entry") {
		t.Error("没引用到的基础类型不应 import")
	}
}
