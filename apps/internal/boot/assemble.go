package boot

// 应用装配（原 apps/ybase/assemble.go，docs/90 - 归档/00 - 总体架构.md 第六节）：按 profile 装配插件，取出内核循环要用的服务。
// CLI（apps/ybase）与桌面端（apps/desktop）共用。

import (
	compact "ybase/modules/context/compact-basic"
	"ybase/runtime/agentdef"
	"ybase/runtime/assemble"
	"ybase/runtime/component"

	// 核心插件：存储、模型、协议适配器、压缩
	_ "ybase/modules/model/adapter-anthropic"
	_ "ybase/modules/model/adapter-openai-chat"
	_ "ybase/modules/model/adapter-openai-responses"
	_ "ybase/modules/model/models"
	_ "ybase/modules/storage/store-sqlite"

	// 插件登记（init 里 component.Register）；装配哪些由 profile 决定
	_ "ybase/modules/collab/subagent"
	_ "ybase/modules/collab/workgroup"
	_ "ybase/modules/context/instructions"
	_ "ybase/modules/context/prompt"
	_ "ybase/modules/context/redact"
	_ "ybase/modules/context/skills"
	_ "ybase/modules/extend/hooks"
	_ "ybase/modules/security/permission"
	_ "ybase/modules/security/sandbox"
	_ "ybase/modules/tools/ask-user"
	_ "ybase/modules/tools/codemode"
	_ "ybase/modules/tools/fs"
	_ "ybase/modules/tools/mcp"
	_ "ybase/modules/tools/shell"
	_ "ybase/modules/tools/todo"
	_ "ybase/modules/tools/web"
)

// Assembled 装配结果里内核循环要用的部分。
type Assembled struct {
	Asm     *component.Assembly
	Store   assemble.DBHandle
	Models  *assemble.Models
	Compact *compact.Compactor // compact-basic（可空）
	// Defs 会话定义的来源：组件内置模板 + 应用平台定义（T1b-2c、03 3.5）。
	Defs agentdef.Source
}

// Assemble 按 profile 装配；overrides 按插件名覆盖配置键（命令行参数），inject 为应用预置服务。
// 插件实例取自进程内注册表。
func Assemble(p *component.Profile, overrides map[string]map[string]any, inject map[string]any) (*Assembled, error) {
	return AssembleInstances(p, overrides, inject, nil)
}

// AssembleInstances 同 Assemble；instances 里给出的插件直接使用实例，不再走注册表
// （ybase.New 的组件清单用，T1b-2d）。
func AssembleInstances(p *component.Profile, overrides map[string]map[string]any, inject map[string]any, instances map[string]component.Component) (*Assembled, error) {
	spec := p.Spec(assemble.SlotDefaults())
	spec.Inject = inject
	if len(instances) > 0 {
		spec.Instances = instances
	}
	for i, e := range spec.Plugins {
		if ov, ok := overrides[e.Name]; ok {
			c := map[string]any{}
			for k, v := range e.Config {
				c[k] = v
			}
			for k, v := range ov {
				c[k] = v
			}
			spec.Plugins[i].Config = c
		}
	}
	asm, err := component.Assemble(spec)
	if err != nil {
		return nil, err
	}
	rt := &Assembled{Asm: asm}
	// 组件登记的内置定义模板归入平台命名空间：模板之间 / 与应用平台定义同名即装配失败（启动报错）。
	defs, err := asm.Definitions(nil)
	if err != nil {
		return nil, err
	}
	rt.Defs = defs
	if v, ok := asm.Value(assemble.ServiceDB); ok {
		rt.Store, _ = v.(assemble.DBHandle)
	}
	if v, ok := asm.Value(assemble.ServiceModelhub); ok {
		rt.Models, _ = v.(*assemble.Models)
	}
	rt.Compact = compact.Wire(asm)
	return rt, nil
}
