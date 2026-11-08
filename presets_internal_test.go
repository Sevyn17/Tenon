package tenon

import (
	"slices"
	"sort"
	"testing"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
)

// 两种入口的插件缺省（计划/07 阶段三）：按 profile 装配（WithProfile，apphost 走这条）时按各插件的 Meta.Default；
// Default() 预设是"全部标准组件、存储除外"——存储由 WithStore 给，或 profile 模式下由存储插件给。
// 新增标准组件要么进 Default()，要么在这里说明为什么不进；这条测试防止两边悄悄分叉。
func TestDefaultIsEveryStandardComponent(t *testing.T) {
	var want []string
	for _, n := range component.Registered() {
		m, ok := component.MetaOf(n)
		if !ok || slices.Contains(m.Provides, assemble.ServiceDB) {
			continue
		}
		want = append(want, n)
	}
	got := append([]string(nil), defaultComponents...)
	sort.Strings(want)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Fatalf("Default() = %v\n已登记的标准组件（除存储）= %v", got, want)
	}
	for _, n := range minimalComponents {
		if !slices.Contains(defaultComponents, n) {
			t.Errorf("Minimal() 的 %s 不在 Default() 里", n)
		}
	}
}
