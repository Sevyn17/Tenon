package tenon

// 内部测试（package tenon）不能 import tenon/std（std 依赖根包，会成环）：
// 这里空导入最小预设要用的模块，按名字装配（Mortise docs/62 §3.3）。

import (
	_ "github.com/Sevyn17/tenon/modules/context/compact-basic"
	_ "github.com/Sevyn17/tenon/modules/context/instructions"
	_ "github.com/Sevyn17/tenon/modules/context/prompt"
	_ "github.com/Sevyn17/tenon/modules/context/redact"
	_ "github.com/Sevyn17/tenon/modules/context/skills"
	_ "github.com/Sevyn17/tenon/modules/model/adapter-anthropic"
	_ "github.com/Sevyn17/tenon/modules/model/adapter-openai-chat"
	_ "github.com/Sevyn17/tenon/modules/model/adapter-openai-responses"
	_ "github.com/Sevyn17/tenon/modules/model/models"
)

// testMinimal 同 std.Minimal()。
func testMinimal() Option {
	return WithComponents("models", "adapter-anthropic", "adapter-openai-chat", "adapter-openai-responses",
		"instructions", "prompt", "redact", "skills", "compact-basic")
}
