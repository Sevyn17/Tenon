package component

// 客户端改进 M10 T18：SchemaOf 给容器类型标出元素 Kind（Field.Elem），界面据此把结构体数组、map 只读显示。

import "testing"

type elemItem struct {
	ID string `cfg:"id"`
}

type elemCfg struct {
	Names  []string            `cfg:"names"`
	Groups []elemItem          `cfg:"groups"`
	Ptrs   []*elemItem         `cfg:"ptrs"`
	Env    map[string]string   `cfg:"env"`
	Nested map[string]elemItem `cfg:"nested"`
	Pair   [2]int              `cfg:"pair"`
	N      int                 `cfg:"n"`
}

func TestSchemaOfElem(t *testing.T) {
	got := map[string][2]string{}
	for _, f := range SchemaOf(&elemCfg{}) {
		got[f.Key] = [2]string{f.Type, f.Elem}
	}
	want := map[string][2]string{
		"names": {"slice", "string"}, "groups": {"slice", "struct"}, "ptrs": {"slice", "struct"},
		"env": {"map", "string"}, "nested": {"map", "struct"}, "pair": {"array", "int"}, "n": {"int", ""},
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s：得到 %v，应为 %v", k, got[k], w)
		}
	}
}
