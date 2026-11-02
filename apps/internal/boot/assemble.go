package boot

// 应用装配（原 apps/ybase/assemble.go，docs/90 - 归档/00 - 总体架构.md 第六节）：按 profile 装配插件，取出内核循环要用的服务。
// CLI（apps/ybase）与桌面端（apps/desktop）共用。

import (
	"ybase-kernel/core/agentdef"
	"ybase-kernel/core/plugin"
	"ybase-kernel/core/runtime"
	compact "ybase-kernel/plugins/context/compact-basic"

	// 核心插件：存储、模型、协议适配器、压缩
	_ "ybase-kernel/plugins/model/adapter-anthropic"
	_ "ybase-kernel/plugins/model/adapter-openai-chat"
	_ "ybase-kernel/plugins/model/adapter-openai-responses"
	_ "ybase-kernel/plugins/model/models"
	_ "ybase-kernel/plugins/storage/store-sqlite"

	// 插件登记（init 里 plugin.Register）；装配哪些由 profile 决定
	_ "ybase-kernel/plugins/collab/subagent"
	_ "ybase-kernel/plugins/collab/workgroup"
	_ "ybase-kernel/plugins/context/instructions"
	_ "ybase-kernel/plugins/context/prompt"
	_ "ybase-kernel/plugins/context/redact"
	_ "ybase-kernel/plugins/context/skills"
	_ "ybase-kernel/plugins/extend/hooks"
	_ "ybase-kernel/plugins/security/permission"
	_ "ybase-kernel/plugins/security/sandbox"
	_ "ybase-kernel/plugins/tools/ask-user"
	_ "ybase-kernel/plugins/tools/codemode"
	_ "ybase-kernel/plugins/tools/fs"
	_ "ybase-kernel/plugins/tools/mcp"
	_ "ybase-kernel/plugins/tools/shell"
	_ "ybase-kernel/plugins/tools/todo"
	_ "ybase-kernel/plugins/tools/web"
)

// Assembled 装配结果里内核循环要用的部分。
type Assembled struct {
	Asm     *plugin.Assembly
	Store   runtime.DBHandle
	Models  *runtime.Models
	Compact *compact.Compactor // compact-basic（可空）
	// Defs 会话定义的来源：组件内置模板 + 应用平台定义（T1b-2c、03 3.5）。
	Defs agentdef.Source
}

// Assemble 按 profile 装配；overrides 按插件名覆盖配置键（命令行参数），inject 为应用预置服务。
func Assemble(p *plugin.Profile, overrides map[string]map[string]any, inject map[string]any) (*Assembled, error) {
	spec := p.Spec(runtime.SlotDefaults())
	spec.Inject = inject
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
	asm, err := plugin.Assemble(spec)
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
	if v, ok := asm.Value(runtime.ServiceDB); ok {
		rt.Store, _ = v.(runtime.DBHandle)
	}
	if v, ok := asm.Value(runtime.ServiceModelhub); ok {
		rt.Models, _ = v.(*runtime.Models)
	}
	rt.Compact = compact.Wire(asm)
	return rt, nil
}
