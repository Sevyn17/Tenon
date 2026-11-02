package ybase

// 预设组件清单（10 10.2）。名字与 plugin.Register 一致；装配顺序由 Requires 拓扑排序决定。
//
// 本包空 import 这些插件：它们各自在 init 里登记，Default / Minimal 才能实例化。
// 存储（store-sqlite）不在预设里：存储由 WithStore 提供。

import (
	_ "ybase-kernel/plugins/collab/subagent"
	_ "ybase-kernel/plugins/collab/workgroup"
	_ "ybase-kernel/plugins/context/compact-basic"
	_ "ybase-kernel/plugins/context/instructions"
	_ "ybase-kernel/plugins/context/prompt"
	_ "ybase-kernel/plugins/context/redact"
	_ "ybase-kernel/plugins/context/skills"
	_ "ybase-kernel/plugins/extend/hooks"
	_ "ybase-kernel/plugins/model/adapter-anthropic"
	_ "ybase-kernel/plugins/model/adapter-openai-chat"
	_ "ybase-kernel/plugins/model/adapter-openai-responses"
	_ "ybase-kernel/plugins/model/models"
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
