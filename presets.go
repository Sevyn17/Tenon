package tenon

// 预设组件清单（10 10.2）。名字与 plugin.Register 一致；装配顺序由 Requires 拓扑排序决定。
//
// 本包空 import 这些插件：它们各自在 init 里登记，Default / Minimal 才能实例化。
// 存储（store-sqlite）不在预设里：存储由 WithStore 提供。

import (
	_ "tenon/modules/collab/subagent"
	_ "tenon/modules/collab/workgroup"
	_ "tenon/modules/context/compact-basic"
	_ "tenon/modules/context/instructions"
	_ "tenon/modules/context/prompt"
	_ "tenon/modules/context/redact"
	_ "tenon/modules/context/skills"
	_ "tenon/modules/extend/hooks"
	_ "tenon/modules/model/adapter-anthropic"
	_ "tenon/modules/model/adapter-openai-chat"
	_ "tenon/modules/model/adapter-openai-responses"
	_ "tenon/modules/model/models"
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

// defaultComponents 标准预设：模型、上下文、权限与沙箱、工具、协作。
var defaultComponents = []string{
	"models",
	"adapter-anthropic",
	"adapter-openai-chat",
	"adapter-openai-responses",
	"instructions",
	"prompt",
	"redact",
	"skills",
	"compact-basic",
	"hooks",
	"permission",
	"sandbox",
	"ask-user",
	"code-mode",
	"fs",
	"mcp",
	"shell",
	"todo",
	"web",
	"subagent",
	"workgroup",
}

// minimalComponents 最小预设：内核 + 上下文（压缩要用模型组件）。
var minimalComponents = []string{
	"models",
	"adapter-anthropic",
	"adapter-openai-chat",
	"adapter-openai-responses",
	"instructions",
	"prompt",
	"redact",
	"skills",
	"compact-basic",
}
