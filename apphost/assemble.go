package apphost

// 登记全部内置插件（空 import，init 里 component.Register）；装配由 SDK 按 profile 做（Start 里 tenon.WithProfile）。
// Assembled 是装配结果里应用常用的几样，给命令行等调用方读。
// 应用宿主：命令行、桌面端、网页服务（client/ 下的三个程序）与任何建在框架上的应用共用。

import (
	compact "github.com/Sevyn17/tenon/modules/context/compact-basic"
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"

	// 标准层（Mortise docs/62 §3.2、K6）：apphost 归标准层，经 tenon/std 登记全部标准模块与它们的协议适配；
	// 装配哪些由 profile 决定。存储插件不在这里登记：它是产品的选择（SQLite、PostgreSQL 或别的），
	// 由调用方在自己的 main 包里空导入。装配时没有组件提供 service:db 会报错。
	_ "github.com/Sevyn17/tenon/std"
)

// Assembled 装配结果里内核循环要用的部分。
type Assembled struct {
	Asm     *component.Assembly
	Store   assemble.DBHandle
	Models  *assemble.Models
	Compact *compact.Compactor // compact-basic（可空）
	// Defs 会话定义的来源：组件内置模板 + 应用平台定义（T1b-2c、03 3.5）。
	Defs agentdef.Source
}
