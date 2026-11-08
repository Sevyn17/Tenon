package apphost

// 应用装配（原 apps/tenon/assemble.go，docs/90 - 归档/00 - 总体架构.md 第六节）：按 profile 装配插件，取出内核循环要用的服务。
// 应用宿主：命令行、桌面端、网页服务（client/ 下的三个程序）与任何建在框架上的应用共用。

import (
	compact "tenon/modules/context/compact-basic"
	"tenon/runtime/agentdef"
	"tenon/runtime/assemble"
	"tenon/runtime/component"

	// 核心插件：存储、模型、协议适配器、压缩
	_ "tenon/modules/model/adapter-anthropic"
	_ "tenon/modules/model/adapter-openai-chat"
	_ "tenon/modules/model/adapter-openai-responses"
	_ "tenon/modules/model/models"
	_ "tenon/modules/storage/store-sqlite"

	// 插件登记（init 里 component.Register）；装配哪些由 profile 决定
	_ "tenon/modules/collab/subagent"
	_ "tenon/modules/collab/workgroup"
	_ "tenon/modules/context/instructions"
	_ "tenon/modules/context/prompt"
	_ "tenon/modules/context/redact"
	_ "tenon/modules/context/skills"
	_ "tenon/modules/extend/hooks"
	_ "tenon/modules/security/permission"
	_ "tenon/modules/security/sandbox"
	_ "tenon/modules/tools/ask-user"
	_ "tenon/modules/tools/codemode"
	_ "tenon/modules/tools/fs"
	_ "tenon/modules/tools/mcp"
	_ "tenon/modules/tools/shell"
	_ "tenon/modules/tools/todo"
	_ "tenon/modules/tools/web"
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
// （tenon.New 的组件清单用，T1b-2d）。
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
	if v, ok := asm.Value(assemble.ServiceCompactor); ok {
		rt.Compact, _ = v.(*compact.Compactor)
	}
	return rt, nil
}
