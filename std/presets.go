package std

// 预设组件清单（10 10.2）。名字与 plugin.Register 一致；装配顺序由 Requires 拓扑排序决定。
//
// 本包空 import 这些插件：它们各自在 init 里登记，Default / Minimal 才能实例化。
// 存储不在预设里：存储由 WithStore 提供，或由产品登记的存储组件提供。

import (
	"github.com/Sevyn17/tenon"

	_ "github.com/Sevyn17/tenon/modules/collab/group"
	_ "github.com/Sevyn17/tenon/modules/collab/subagent"
	_ "github.com/Sevyn17/tenon/modules/collab/workgroup"
	_ "github.com/Sevyn17/tenon/modules/context/compact-basic"
	_ "github.com/Sevyn17/tenon/modules/context/instructions"
	_ "github.com/Sevyn17/tenon/modules/context/prompt"
	_ "github.com/Sevyn17/tenon/modules/context/redact"
	_ "github.com/Sevyn17/tenon/modules/context/skills"
	_ "github.com/Sevyn17/tenon/modules/extend/budget"
	_ "github.com/Sevyn17/tenon/modules/extend/events"
	_ "github.com/Sevyn17/tenon/modules/extend/extproc"
	_ "github.com/Sevyn17/tenon/modules/extend/hooks"
	_ "github.com/Sevyn17/tenon/modules/extend/modelmatch"
	_ "github.com/Sevyn17/tenon/modules/model/adapter-anthropic"
	_ "github.com/Sevyn17/tenon/modules/model/adapter-openai-chat"
	_ "github.com/Sevyn17/tenon/modules/model/adapter-openai-responses"
	_ "github.com/Sevyn17/tenon/modules/model/models"
	_ "github.com/Sevyn17/tenon/modules/security/permission"
	_ "github.com/Sevyn17/tenon/modules/security/sandbox"
	_ "github.com/Sevyn17/tenon/modules/tools/ask-user"
	_ "github.com/Sevyn17/tenon/modules/tools/checkpoint"
	_ "github.com/Sevyn17/tenon/modules/tools/codemode"
	_ "github.com/Sevyn17/tenon/modules/tools/fs"
	_ "github.com/Sevyn17/tenon/modules/tools/mcp"
	_ "github.com/Sevyn17/tenon/modules/tools/planmode"
	_ "github.com/Sevyn17/tenon/modules/tools/shell"
	_ "github.com/Sevyn17/tenon/modules/tools/todo"
	_ "github.com/Sevyn17/tenon/modules/tools/web"
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
	"checkpoint",
	"fs",
	"mcp",
	"plan-mode",
	"shell",
	"todo",
	"web",
	"subagent",
	"workgroup",
	"group",
	"events",
	"budget",
	"extproc",
	"modelmatch",
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

// Default 标准组件预设（10 10.2）：内核、上下文、模型、工具、权限、沙箱、协作。
// 不含存储：存储由 tenon.WithStore 提供。原 tenon.Default（Mortise docs/62 K6 迁到这里）。
func Default() tenon.Option { return tenon.WithComponents(defaultComponents...) }

// Minimal 最小预设（10 10.2）：内核 + 上下文（含压缩所需的模型组件）。原 tenon.Minimal。
func Minimal() tenon.Option { return tenon.WithComponents(minimalComponents...) }

// DefaultNames / MinimalNames 两组预设的组件名（副本）。
func DefaultNames() []string { return append([]string(nil), defaultComponents...) }

// MinimalNames 见 DefaultNames。
func MinimalNames() []string { return append([]string(nil), minimalComponents...) }
