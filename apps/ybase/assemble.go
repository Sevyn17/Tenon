package main

// 应用装配（docs/90 - 归档/00 - 总体架构.md 第六节）。实现已移到 apps/internal/boot（桌面端与 CLI 共用），这里保留原名。

import (
	"ybase-kernel/apps/internal/boot"
	"ybase-kernel/core/plugin"
)

// assembled 装配结果里内核循环要用的部分。
type assembled = boot.Assembled

// assemble 按 profile 装配；overrides 按插件名覆盖配置键（命令行参数），inject 为应用预置服务。
func assemble(p *plugin.Profile, overrides map[string]map[string]any, inject map[string]any) (*assembled, error) {
	return boot.Assemble(p, overrides, inject)
}
