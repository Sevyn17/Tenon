package main

// 应用装配（docs/90 - 归档/00 - 总体架构.md 第六节）。实现已移到 apps/internal/boot（桌面端与 CLI 共用），这里保留原名。

import "ybase/apps/internal/boot"

// assembled 装配结果里内核循环要用的部分。
type assembled = boot.Assembled
