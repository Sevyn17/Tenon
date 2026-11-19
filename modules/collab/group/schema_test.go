package group

// 客户端改进 M10 T18：groups 是结构体数组，plugins.list 的 schema 要标出 elem=struct，界面才知道只读显示、
// 保存时不把它放进 plugins.set。

import (
	"testing"

	"github.com/Sevyn17/tenon/runtime/component"
)

func TestGroupsSchemaElemStruct(t *testing.T) {
	m, ok := component.MetaOf(ServiceName)
	if !ok {
		t.Fatal("group 未登记")
	}
	if m.Description == "" || m.Version == "" {
		t.Fatalf("应有版本和说明：%+v", m)
	}
	for _, f := range component.SchemaOf(m.Config) {
		if f.Key == "groups" {
			if f.Type != "slice" || f.Elem != "struct" {
				t.Fatalf("groups：type=%s elem=%s", f.Type, f.Elem)
			}
			return
		}
	}
	t.Fatal("schema 里没有 groups")
}
