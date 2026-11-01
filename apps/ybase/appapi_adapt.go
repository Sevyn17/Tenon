package main

// 插件能力 → appapi 小接口的适配。实现已移到 apps/internal/boot（PermModes，桌面端与 CLI 共用）。

import (
	"ybase-kernel/apps/internal/boot"
	"ybase-kernel/plugins/security/permission"
)

// newPermModes 用 permission 插件的会话服务实现 appapi.Modes；workdir 用于项目档位的确认记录（trust.toml）。
func newPermModes(p *permission.Sessions, workdir string) boot.PermModes {
	return boot.PermModes{P: p, Workdir: workdir}
}
