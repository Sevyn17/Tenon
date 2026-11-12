package permission

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// fakeLayers 测试用配置来源：层按给定顺序返回。
type fakeLayers []component.ConfigLayer

func (f fakeLayers) Layers(context.Context) ([]component.ConfigLayer, error) { return f, nil }

// nilDB 装配用的存储句柄：DB() 返回 nil，权限组件遇到 nil 不读会话状态。
type nilDB struct{}

func (nilDB) DB() store.Store          { return nil }
func (nilDB) Blobs() toolhub.Artifacts { return nil }

func permLayer(origin string, trusted bool, body map[string]any) component.ConfigLayer {
	return component.ConfigLayer{Name: origin, Origin: origin, Trusted: trusted, Where: origin,
		Values: map[string]any{"permissions": body}}
}

// assemblePerm 只装 permission；layers 为 nil 时不注入配置来源。
func assemblePerm(t *testing.T, layers component.LayeredConfig) *Sessions {
	t.Helper()
	p := &plug{}
	inject := map[string]any{
		assemble.ServiceWorkdir: t.TempDir(),
		assemble.ServiceDB:      nilDB{},
	}
	if layers != nil {
		inject[component.KeyLayers.Name()] = layers
	}
	a, err := component.Assemble(component.Spec{
		Plugins:   []component.Entry{{Name: "permission"}},
		Inject:    inject,
		Instances: map[string]component.Component{"permission": p},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := a.Value(Service)
	if !ok {
		t.Fatal("permission 没提供服务")
	}
	return v.(*Sessions)
}

// 18 方案第四节：permission 不再自己读文件，从配置层取。
// 项目层不受信不用；受信时规则累加，后者的 mode 覆盖前者。
func TestPermissionFromLayers(t *testing.T) {
	s := assemblePerm(t, fakeLayers{
		permLayer(component.OriginUser, true, map[string]any{"mode": "ask", "deny": []any{"shell(rm:*)"}}),
		permLayer(component.OriginProject, true, map[string]any{"mode": "read-only", "ask": []any{"shell(git push:*)"}}),
	})
	if s.Default != ReadOnly {
		t.Fatalf("项目层的 mode 应覆盖用户层，得 %s", s.Default)
	}
	if n := len(s.Rules); n != 2 {
		t.Fatalf("两层规则应累加，得 %d 条", n)
	}

	// 项目不受信：项目层的档位不生效。
	s = assemblePerm(t, fakeLayers{
		permLayer(component.OriginProject, false, map[string]any{"mode": "read-only"}),
	})
	if s.Default != Default {
		t.Fatalf("不受信项目层不应生效，得 %s", s.Default)
	}
}

// 项目层写 allow 照样报错（公共规则只能加严，与 LoadConfig 同一套检查）。
func TestPermissionLayerRejectsAllow(t *testing.T) {
	p := &plug{}
	_, err := component.Assemble(component.Spec{
		Plugins: []component.Entry{{Name: "permission"}},
		Inject: map[string]any{
			assemble.ServiceWorkdir:    t.TempDir(),
			assemble.ServiceDB:         nilDB{},
			component.KeyLayers.Name(): fakeLayers{permLayer(component.OriginProject, true, map[string]any{"allow": []any{"shell"}})},
		},
		Instances: map[string]component.Component{"permission": p},
	})
	if err == nil {
		t.Fatal("项目层写 allow 应报错")
	}
}

// 没注入配置来源：没有外部规则（嵌入场景的缺省）。
func TestPermissionWithoutLayers(t *testing.T) {
	s := assemblePerm(t, nil)
	if s.Default != Default || len(s.Rules) != 0 {
		t.Fatalf("没有配置来源时应无外部规则，得 mode=%s rules=%d", s.Default, len(s.Rules))
	}
}
