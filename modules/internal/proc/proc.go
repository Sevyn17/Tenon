// Package proc 标准模块共用的子进程工具：选 shell、PowerShell 包装、杀进程树
// （2026-11-04 审查 P1-29：原先散在 shell、hooks、mcp 三处，只有 shell 杀进程树；
// hooks 超时会留下孙进程，mcp 的 npx 类服务关闭时子进程还占着管道）。
// Go internal 包：只有 modules/ 下的组件能 import，不算组件之间互相依赖（见 runtime/assemble/deps_test.go）。
package proc

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// psUTF8 强制 PowerShell 以 UTF-8 输出（修中文 Windows 的 GBK 乱码）。
const psUTF8 = "try { [Console]::OutputEncoding=[System.Text.Encoding]::UTF8; $OutputEncoding=[System.Text.Encoding]::UTF8 } catch {}\n"

// psExitPre / psExitPost 让 PowerShell 的退出码等于最后一条命令的结果：最后一条是原生程序且失败时用它的真实退出码。
// 不加时 `powershell -Command` 把原生程序的任何非零退出码都报成 1（`cmd /c exit 2` → 1），
// 钩子的"退出码 2 = 阻止"在 Windows 上静默失效，shell 报给模型的退出码也失真（审查 P0-15）。
// 只在最后一条失败（$? 为假）时生效：成功仍是 0，cmdlet 失败仍是 1，显式 exit N 原样。
const (
	psExitPre  = "$global:LASTEXITCODE = 0\n"
	psExitPost = "\nif (-not $?) { if ($LASTEXITCODE) { exit $LASTEXITCODE }; exit 1 }"
)

// PSCommand 包装成 PowerShell -Command 的参数：utf8 为真时先切 UTF-8 输出；总是带真实退出码。
func PSCommand(c string, utf8 bool) string {
	if utf8 {
		return psUTF8 + psExitPre + c + psExitPost
	}
	return psExitPre + c + psExitPost
}

// PackagedApp 报告可执行文件是否是 Windows 商店（MSIX）打包应用的执行别名或包内文件。
// 打包应用跑在包容器里：不继承调用方的当前目录，%LOCALAPPDATA% 等目录被虚拟化成包内的私有视图，
// 看不到调用方刚建的目录和文件（2026-11-03 演示）。这类 shell 不能当执行环境，退回系统自带的 Windows PowerShell。
func PackagedApp(p string) bool {
	return strings.Contains(strings.ToLower(filepath.ToSlash(p)), "/windowsapps/")
}

// ShellArgv 返回把一条命令交给本平台 shell 执行的 argv 构造函数：
// Windows 用 pwsh（非商店版）或 powershell，包一层真实退出码；其他平台用 bash 或 sh。
func ShellArgv(utf8 bool) func(string) []string {
	if runtime.GOOS == "windows" {
		bin := "powershell"
		if p, err := exec.LookPath("pwsh"); err == nil && !PackagedApp(p) {
			bin = p
		}
		return func(c string) []string {
			return []string{bin, "-NoProfile", "-NonInteractive", "-Command", PSCommand(c, utf8)}
		}
	}
	bin := "sh"
	if p, err := exec.LookPath("bash"); err == nil {
		bin = p
	}
	return func(c string) []string { return []string{bin, "-c", c} }
}

// SetTree 让 cmd 被取消时杀整个进程树。cmd 必须由 exec.CommandContext 创建（Go 不允许给普通
// exec.Command 设 Cancel，Start 会报错）；普通命令用 Group，关闭时调 Kill。
func SetTree(cmd *exec.Cmd) {
	group(cmd)
	cmd.Cancel = func() error { return Kill(cmd) }
}

// Group 只把 cmd 放进新进程组（不设 Cancel），用于 exec.Command 创建、由调用方自己 Kill 的命令。
func Group(cmd *exec.Cmd) { group(cmd) }

// Kill 杀掉 cmd 的整个进程树（cmd 须已 SetTree / Group 并 Start）；没启动时什么都不做。
func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return killTree(cmd)
}
