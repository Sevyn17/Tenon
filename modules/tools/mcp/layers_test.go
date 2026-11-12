package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
)

type fakeLayers []component.ConfigLayer

func (f fakeLayers) Layers(context.Context) ([]component.ConfigLayer, error) { return f, nil }

type testUI struct{}

func (testUI) Print(string)        {}
func (testUI) Warn(string, string) {}
func (testUI) Ask(context.Context, string, []string) (string, error) {
	return "", errors.New("no ui")
}

func mcpLayer(origin string, trusted bool, servers ...map[string]any) component.ConfigLayer {
	return component.ConfigLayer{Name: origin, Origin: origin, Trusted: trusted, Where: origin,
		Values: map[string]any{"mcp": map[string]any{"server": servers}}}
}

func server(name, url string) map[string]any {
	return map[string]any{"name": name, "url": url}
}

func assembleMCP(t *testing.T, layers component.LayeredConfig) *plug {
	t.Helper()
	p := &plug{}
	inject := map[string]any{assemble.ServiceUI: testUI{}}
	if layers != nil {
		inject[component.KeyLayers.Name()] = layers
	}
	if _, err := component.Assemble(component.Spec{
		Plugins:   []component.Entry{{Name: "mcp"}},
		Inject:    inject,
		Instances: map[string]component.Component{"mcp": p},
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

// 18 方案第七节：mcp 只认项目层，且项目层不受信不用。用户层即使写了也不生效。
func TestMCPFromLayers(t *testing.T) {
	p := assembleMCP(t, fakeLayers{
		mcpLayer(component.OriginUser, true, server("user", "http://user.example")),
		mcpLayer(component.OriginProject, true, server("proj", "http://proj.example")),
	})
	if n := len(p.cfg.Server); n != 1 || p.cfg.Server[0].Name != "proj" {
		t.Fatalf("只应有受信项目层的服务，得 %+v", p.cfg.Server)
	}

	p = assembleMCP(t, fakeLayers{
		mcpLayer(component.OriginProject, false, server("proj", "http://proj.example")),
	})
	if n := len(p.cfg.Server); n != 0 {
		t.Fatalf("不受信项目层不应加载，得 %d 个服务", n)
	}
}

// 项目层的服务照样校验（与文件、插件包共用 checkServers）。
func TestMCPLayerValidates(t *testing.T) {
	bad := mcpLayer(component.OriginProject, true, map[string]any{"name": "bad name", "url": "http://x"})
	p := &plug{}
	_, err := component.Assemble(component.Spec{
		Plugins: []component.Entry{{Name: "mcp"}},
		Inject: map[string]any{
			assemble.ServiceUI:         testUI{},
			component.KeyLayers.Name(): fakeLayers{bad},
		},
		Instances: map[string]component.Component{"mcp": p},
	})
	if err == nil {
		t.Fatal("非法服务名应报错")
	}
}

// 没注入配置来源：没有外部服务（嵌入场景的缺省）。
func TestMCPWithoutLayers(t *testing.T) {
	p := assembleMCP(t, nil)
	if n := len(p.cfg.Server); n != 0 {
		t.Fatalf("没有配置来源时应无服务，得 %d 个", n)
	}
}
