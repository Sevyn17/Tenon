package ybase

// 预设组件清单（10 10.2）。名字与 plugin.Register 一致；装配顺序由 Requires 拓扑排序决定。
//
// 本包空 import 这些插件：它们各自在 init 里登记，Default / Minimal 才能实例化。
// 存储（store-sqlite）不在预设里：存储由 WithStore 提供。

import (
	_ "ybase/modules/collab/subagent"
	_ "ybase/modules/collab/workgroup"
	_ "ybase/modules/context/compact-basic"
	_ "ybase/modules/context/instructions"
	_ "ybase/modules/context/prompt"
	_ "ybase/modules/context/redact"
	_ "ybase/modules/context/skills"
	_ "ybase/modules/extend/hooks"
	_ "ybase/modules/model/adapter-anthropic"
	_ "ybase/modules/model/adapter-openai-chat"
	_ "ybase/modules/model/adapter-openai-responses"
	_ "ybase/modules/model/models"
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
