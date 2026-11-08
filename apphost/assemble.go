package apphost

// 登记全部内置插件（空 import，init 里 component.Register）；装配由 SDK 按 profile 做（Start 里 tenon.WithProfile）。
// Assembled 是装配结果里应用常用的几样，给命令行等调用方读。
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
