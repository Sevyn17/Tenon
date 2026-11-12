package hooks

import (
	"context"
	"errors"
	"testing"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
)

// fakeLayers 测试用的配置来源：层按给定顺序返回。
type fakeLayers []component.ConfigLayer

func (f fakeLayers) Layers(context.Context) ([]component.ConfigLayer, error) { return f, nil }

type testUI struct{}

func (testUI) Print(string)        {}
func (testUI) Warn(string, string) {}
func (testUI) Ask(context.Context, string, []string) (string, error) {
	return "", errors.New("no ui")
}

func hookLayer(origin string, trusted bool, cmd string) component.ConfigLayer {
	return component.ConfigLayer{Name: origin, Origin: origin, Trusted: trusted,
		Values: map[string]any{"hooks": map[string]any{
			"hook": []map[string]any{{"event": "stop", "command": cmd}}}}}
}

// assembleHooks 只装 hooks 一个组件；layers 为 nil 时不注入配置来源。
func assembleHooks(t *testing.T, projectHooks bool, layers component.LayeredConfig) *plug {
	t.Helper()
	p := &plug{}
	inject := map[string]any{assemble.ServiceWorkdir: t.TempDir(), assemble.ServiceUI: testUI{}}
	if layers != nil {
		inject[component.KeyLayers.Name()] = layers
	}
	_, err := component.Assemble(component.Spec{
		Plugins:   []component.Entry{{Name: "hooks", Config: map[string]any{"project_hooks": projectHooks}}},
		Inject:    inject,
		Instances: map[string]component.Component{"hooks": p},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// 18 方案第四节：hooks 不再自己读文件，从配置层取。
// 用户层总是加载且不脱敏；项目层是执行闸门（project_hooks 或层标受信才加载）且脱敏；
// 脱敏跟来源走，跟层的先后无关。
func TestHooksFromLayers(t *testing.T) {
	// 故意把项目层放在前面：顺序照来源给的走，但脱敏只看来源。
	p := assembleHooks(t, false, fakeLayers{
		hookLayer(component.OriginProject, true, "proj"),
		hookLayer(component.OriginUser, true, "user"),
	})
	hs := p.r.Cfg.Hook
	if len(hs) != 2 || hs[0].Command != "proj" || hs[1].Command != "user" {
		t.Fatalf("应按来源顺序得到 proj、user：%+v", hs)
	}
	if !hs[0].scrub || hs[1].scrub {
		t.Fatalf("项目来源应脱敏、用户来源不脱敏：proj=%v user=%v", hs[0].scrub, hs[1].scrub)
	}

	// 项目不受信且没开 project_hooks：项目层不加载。
	p = assembleHooks(t, false, fakeLayers{hookLayer(component.OriginProject, false, "proj")})
	if n := len(p.r.Cfg.Hook); n != 0 {
		t.Fatalf("不受信项目层不应加载，得 %d 条", n)
	}
	// 显式 project_hooks：不受信也加载，且脱敏。
	p = assembleHooks(t, true, fakeLayers{hookLayer(component.OriginProject, false, "proj")})
	if hs := p.r.Cfg.Hook; len(hs) != 1 || !hs[0].scrub {
		t.Fatalf("project_hooks 应强制加载并脱敏：%+v", hs)
	}
}

// 配置层里的钩子照样校验（与文件、插件包共用 checkHooks）。
func TestHooksFromLayersValidates(t *testing.T) {
	bad := component.ConfigLayer{Name: "user", Origin: component.OriginUser, Trusted: true,
		Values: map[string]any{"hooks": map[string]any{
			"hook": []map[string]any{{"event": "nope", "command": "x"}}}}}
	p := &plug{}
	_, err := component.Assemble(component.Spec{
		Plugins:   []component.Entry{{Name: "hooks"}},
		Inject:    map[string]any{assemble.ServiceWorkdir: t.TempDir(), assemble.ServiceUI: testUI{}, component.KeyLayers.Name(): fakeLayers{bad}},
		Instances: map[string]component.Component{"hooks": p},
	})
	if err == nil {
		t.Fatal("非法 event 应报错")
	}
}

// 没注入配置来源：不从外部取配置（嵌入场景的缺省）。
func TestHooksWithoutLayers(t *testing.T) {
	p := assembleHooks(t, false, nil)
	if n := len(p.r.Cfg.Hook); n != 0 {
		t.Fatalf("没有配置来源时应无钩子，得 %d 条", n)
	}
}
