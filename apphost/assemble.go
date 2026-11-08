package apphost

// 登记全部内置插件（空 import，init 里 component.Register）；装配由 SDK 按 profile 做（Start 里 tenon.WithProfile）。
// Assembled 是装配结果里应用常用的几样，给命令行等调用方读。
// 应用宿主：命令行、桌面端、网页服务（client/ 下的三个程序）与任何建在框架上的应用共用。

import (
	compact "github.com/Sevyn17/tenon/modules/context/compact-basic"
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"

	// 核心插件：存储、模型、协议适配器、压缩
	_ "github.com/Sevyn17/tenon/modules/model/adapter-anthropic"
	_ "github.com/Sevyn17/tenon/modules/model/adapter-openai-chat"
	_ "github.com/Sevyn17/tenon/modules/model/adapter-openai-responses"
	_ "github.com/Sevyn17/tenon/modules/model/models"
	_ "github.com/Sevyn17/tenon/modules/storage/store-sqlite"

	// 插件登记（init 里 component.Register）；装配哪些由 profile 决定
	_ "github.com/Sevyn17/tenon/modules/collab/subagent"
	_ "github.com/Sevyn17/tenon/modules/collab/workgroup"
	_ "github.com/Sevyn17/tenon/modules/context/instructions"
	_ "github.com/Sevyn17/tenon/modules/context/prompt"
	_ "github.com/Sevyn17/tenon/modules/context/redact"
	_ "github.com/Sevyn17/tenon/modules/context/skills"
	_ "github.com/Sevyn17/tenon/modules/extend/hooks"
	_ "github.com/Sevyn17/tenon/modules/security/permission"
	_ "github.com/Sevyn17/tenon/modules/security/sandbox"
	_ "github.com/Sevyn17/tenon/modules/tools/ask-user"
	_ "github.com/Sevyn17/tenon/modules/tools/codemode"
	_ "github.com/Sevyn17/tenon/modules/tools/fs"
	_ "github.com/Sevyn17/tenon/modules/tools/mcp"
	_ "github.com/Sevyn17/tenon/modules/tools/shell"
	_ "github.com/Sevyn17/tenon/modules/tools/todo"
	_ "github.com/Sevyn17/tenon/modules/tools/web"
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
