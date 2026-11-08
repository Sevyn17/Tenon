package shell

// 命令拆分与启动器停止线在 modules/internal/cmdline（与 permission 共用，审查 P1-61）。

import "github.com/Sevyn17/tenon/modules/internal/cmdline"

// 包内沿用的短名。
var (
	SplitCommand = cmdline.SplitCommand
	Safe         = cmdline.Safe
	Launcher     = cmdline.Launcher
)
