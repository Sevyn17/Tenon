package models

// slot:model_sources（客户端改进 M3，配置T3）：不填插槽时与以前一样；填了时缺省模型到 Start 再定，
// 能用到别的组件在 Setup 里注册的模型（如平台网关）；配置写的模型解析不了时回落并说明。

import (
	"context"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/modelhub"
)

// fakeSources 模型来源：一套 BYOK 式的提供方 + 自动策略。
type fakeSources struct {
	cat  modelhub.CatalogFile
	pick func(*modelhub.Catalog) (string, string)
}

func (f fakeSources) Catalog() modelhub.CatalogFile { return f.cat }
func (f fakeSources) DefaultModel(c *modelhub.Catalog) (string, string) {
	if f.pick == nil {
		return "", ""
	}
	return f.pick(c)
}

type srcComp struct{ s fakeSources }

func (srcComp) Meta() component.Meta {
	return component.Meta{Name: "zz-src", Provides: []string{assemble.SlotModelSources}}
}
func (c srcComp) Setup(cx *component.Registrar) error {
	return cx.FillSlot(strings.TrimPrefix(assemble.SlotModelSources, "slot:"), assemble.ModelSources(c.s))
}
func (srcComp) Start(*component.Scope) error { return nil }

// gwComp 模拟平台网关：依赖 modelhub，在 Setup 里往目录插件层注册 gw/default。
type gwComp struct{}

func (gwComp) Meta() component.Meta {
	return component.Meta{Name: "zz-gw", Requires: []string{assemble.ServiceModelhub}}
}
func (gwComp) Setup(cx *component.Registrar) error {
	m, err := component.Use[*assemble.Models](cx, assemble.ServiceModelhub)
	if err != nil {
		return err
	}
	return m.Hub.Catalog().Add(modelhub.LayerPlugin, "gw", modelhub.CatalogFile{
		Provider: map[string]modelhub.Provider{"gw": {Protocol: "x-gw"}},
		Model:    map[string]modelhub.Model{"gw/default": {Provider: "gw", Name: "default"}},
	})
}
func (gwComp) Start(*component.Scope) error { return nil }

var byokFile = modelhub.CatalogFile{
	Provider: map[string]modelhub.Provider{"byok-1": {Protocol: "openai-chat", BaseURL: "http://v", Key: "byok:1"}},
	// 与内置 deepseek/deepseek-chat 同名：裸名 deepseek-chat 仍指内置那个
	Model: map[string]modelhub.Model{"byok-1/deepseek-chat": {Provider: "byok-1", Name: "deepseek-chat", Label: "日常 · deepseek-chat"}},
}

// assembleStart 装配并启动；cfg 是 models 插件的配置。
func assembleStart(t *testing.T, cfg map[string]any, extra map[string]component.Component) (*assemble.Models, error) {
	t.Helper()
	plugins := []component.Entry{{Name: "models", Config: cfg}}
	for n := range extra {
		plugins = append(plugins, component.Entry{Name: n})
	}
	asm, err := component.Assemble(component.Spec{Plugins: plugins, Instances: extra, Defaults: assemble.SlotDefaults()})
	if err != nil {
		return nil, err
	}
	root := component.NewRoot("t")
	t.Cleanup(func() { _ = root.Close(context.Background()) })
	if err := asm.Start(root); err != nil {
		return nil, err
	}
	v, _ := asm.Value(assemble.ServiceModelhub)
	return v.(*assemble.Models), nil
}

func TestModelSourcesSlot(t *testing.T) {
	// 不填插槽：与以前一样，缺省 deepseek-chat，配置写错就装配失败
	m, err := assembleStart(t, nil, nil)
	if err != nil || m.Model != DefaultModel || m.Origin != assemble.OriginBuiltin {
		t.Fatalf("不填插槽：%+v %v", m, err)
	}
	if m, err := assembleStart(t, map[string]any{"model": "deepseek-reasoner"}, nil); err != nil || m.Model != "deepseek-reasoner" || m.Origin != assemble.OriginConfig {
		t.Fatalf("不填插槽、配置写了模型：%+v %v", m, err)
	}
	if _, err := assembleStart(t, map[string]any{"model": "nope"}, nil); err == nil {
		t.Fatal("不填插槽时配置写错应装配失败（行为不变）")
	}

	// 填了插槽、自动策略没主意：内置缺省；目录里有插槽给的 BYOK 模型，裸名仍指内置
	src := fakeSources{cat: byokFile}
	m, err = assembleStart(t, nil, map[string]component.Component{"zz-src": srcComp{src}})
	if err != nil || m.Model != DefaultModel || m.Origin != assemble.OriginBuiltin || m.Notice != "" {
		t.Fatalf("插槽无主意：%+v %v", m, err)
	}
	if plan, err := m.Hub.Catalog().Resolve(DefaultModel); err != nil || plan[0].Model != "deepseek/deepseek-chat" {
		t.Fatalf("裸名应指内置模型：%v %v", plan, err)
	}

	// 自动策略选 BYOK
	src.pick = func(*modelhub.Catalog) (string, string) {
		return "byok-1/deepseek-chat", "自动：我的模型的默认配置"
	}
	m, err = assembleStart(t, nil, map[string]component.Component{"zz-src": srcComp{src}})
	if err != nil || m.Model != "byok-1/deepseek-chat" || m.Origin != assemble.OriginAuto || m.OriginRef != "自动：我的模型的默认配置" {
		t.Fatalf("自动选 BYOK：%+v %v", m, err)
	}

	// 自动策略能看到别的组件 Setup 里注册的模型（缺省模型在 Start 才定）
	src.pick = func(c *modelhub.Catalog) (string, string) {
		if _, err := c.Resolve("gw/default"); err == nil {
			return "gw/default", "自动：已登录平台"
		}
		return "", ""
	}
	m, err = assembleStart(t, nil, map[string]component.Component{"zz-src": srcComp{src}, "zz-gw": gwComp{}})
	if err != nil || m.Model != "gw/default" || m.Origin != assemble.OriginAuto {
		t.Fatalf("自动选网关：%+v %v", m, err)
	}

	// 配置写的模型优先；它在目录里找不到时回落自动策略并说明，不让装配失败
	m, err = assembleStart(t, map[string]any{"model": "deepseek-reasoner"}, map[string]component.Component{"zz-src": srcComp{src}, "zz-gw": gwComp{}})
	if err != nil || m.Model != "deepseek-reasoner" || m.Origin != assemble.OriginConfig || m.Notice != "" {
		t.Fatalf("配置优先：%+v %v", m, err)
	}
	m, err = assembleStart(t, map[string]any{"model": "byok-gone/x"}, map[string]component.Component{"zz-src": srcComp{src}, "zz-gw": gwComp{}})
	if err != nil || m.Model != "gw/default" || m.Origin != assemble.OriginAuto || !strings.Contains(m.Notice, "byok-gone/x") {
		t.Fatalf("配置的模型不在了应回落自动：%+v %v", m, err)
	}
	m, err = assembleStart(t, map[string]any{"model": "byok-gone/x"}, map[string]component.Component{"zz-src": srcComp{fakeSources{cat: byokFile}}})
	if err != nil || m.Model != DefaultModel || m.Origin != assemble.OriginBuiltin || m.Notice == "" {
		t.Fatalf("自动也没主意时回落内置：%+v %v", m, err)
	}
}
