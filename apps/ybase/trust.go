package main

// 项目信任（plugin-switch-plan §3.3）。实现已移到 apps/internal/boot（桌面端与 CLI 共用），
// 这里只留终端交互（首次询问、ybase trust 子命令）和原名的薄包装，CLI 行为不变。

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ybase-kernel/apps/internal/boot"
	"ybase-kernel/core/plugin"
)

const (
	trustTrusted   = boot.Trusted
	trustUntrusted = boot.Untrusted
)

func modeApproved(root, mode, widened string) bool     { return boot.ModeApproved(root, mode, widened) }
func approveMode(root, mode, widened string) error     { return boot.ApproveMode(root, mode, widened) }
func projectKey(root string) string                    { return boot.ProjectKey(root) }
func normPath(p string) string                         { return boot.NormPath(p) }
func loadTrust() (map[string]string, error)            { return boot.LoadTrust() }
func lookupTrust(m map[string]string, k string) string { return boot.LookupTrust(m, k) }
func saveTrust(key, level string) error                { return boot.SaveTrust(key, level) }

// askTrustStdin 在终端里问一次是否信任项目。
func askTrustStdin(key string) bool {
	fmt.Printf("项目 %s 带有 .ybase 配置（profile / 权限 / 钩子 / MCP，可能执行仓库里写的命令）。\n信任这个项目吗？[y/N] ", key)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes")
}

// resolveTrust 决定本次运行是否信任项目。-trust-project / -project-hooks 只对本次生效。
// 无记录且有项目配置时，交互终端里问一次并保存；非交互按不受信。
func resolveTrust(o opts, root string, interactive bool) (bool, string, error) {
	var ask func(string) bool
	if interactive {
		ask = askTrustStdin
	}
	return boot.ResolveTrust(o.trustProject || o.projectHooks, root, ask)
}

// loadProfiles 内置 < ~/.ybase/profile.toml < <dir>/.ybase/profile.toml（受信时）< -profile。
func loadProfiles(o opts, root string, trusted bool) (*plugin.Profile, []string, error) {
	return boot.LoadProfiles(o.profile, root, trusted)
}

// interactive 标准输入是终端（首次信任询问只在交互运行时做）。
var interactive = func() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

// trustCmd ybase trust [-untrust | -forget] [dir]：保存 / 撤销项目信任记录。
func trustCmd(args []string, workdir string) error {
	level, dir := trustTrusted, workdir
	for _, a := range args {
		switch a {
		case "-untrust", "--untrust":
			level = trustUntrusted
		case "-forget", "--forget":
			level = ""
		default:
			dir = a
		}
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	key := projectKey(root)
	if err := saveTrust(key, level); err != nil {
		return err
	}
	switch level {
	case trustTrusted:
		fmt.Printf("已信任 %s\n", key)
	case trustUntrusted:
		fmt.Printf("已标记为不受信 %s\n", key)
	default:
		fmt.Printf("已删除 %s 的信任记录\n", key)
	}
	return nil
}
