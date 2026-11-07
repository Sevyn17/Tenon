package tenon

import (
	"testing"

	"tenon/runtime/assemble"
	"tenon/runtime/store/memstore"
)

// 工作目录缺省不受信：嵌入方指向任意目录时，不执行其中的 .tenon/hooks.toml、mcp.toml，
// 不读项目档位与项目技能（2026-11-04 审查 P1-5：原先 SDK 写死受信）。
func TestProjectTrustDefaultsFalse(t *testing.T) {
	for _, tc := range []struct {
		opts []Option
		want bool
	}{{nil, false}, {[]Option{WithProjectTrust(true)}, true}} {
		c := &config{}
		for _, o := range append([]Option{Minimal(), WithStore(memstore.New()), WithWorkdir(t.TempDir())}, tc.opts...) {
			if err := o(c); err != nil {
				t.Fatal(err)
			}
		}
		a, err := build(c)
		if a == nil {
			t.Fatalf("build: %v", err)
		}
		v, _ := a.asm.Value(assemble.ServiceTrust)
		if v != tc.want {
			t.Fatalf("service:trust = %v，期望 %v", v, tc.want)
		}
	}
}
