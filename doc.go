// Package tenon 是 Tenon 智能体框架的 SDK 入口。应用只 import 本包（测试再加 tenon/tenontest）。
//
// 公开面清单与版本规则见 docs/01 - 设计文档/框架设计/07 - SDK 接口.md，上手教程见 docs/00 - 上手指南/。
//
// # 建应用
//
// 一个应用 = 组件预设 + 存储 + 模型 + 智能体定义：
//
//	app, err := tenon.New(tenon.Default(),
//	    tenon.WithStore(tenon.MemoryStore()),
//	    tenon.WithOpenAI(baseURL, model, tenon.Env("TENON_API_KEY")))
//	if err != nil { ... }
//	if err := app.Start(ctx); err != nil { ... }
//	defer app.Close(ctx)
//	r, err := app.Ask(ctx, "", "你好", nil)
//
// [New] 只装配与校验，不启动后台工作；校验错误（未知组件、未知配置键、定义写错……）在 New 里一次报完。
// 未 [App.Start] 时调用运行接口返回 [ErrNotStarted]。组件预设：[Default]、[Minimal]；增减组件：[With]、[Without]。
// 组件配置走代码（[WithConfig]），本包不读配置文件，也不做环境变量覆盖。
//
// # 应用选项
//
//   - 存储：[WithStore]、[MemoryStore]、[SQLiteStore]。
//   - 模型：[WithOpenAI]、[WithOpenAIResponses]、[WithAnthropic]、[WithModel]、[WithModels]、[WithModelCatalog]；
//     密钥只以引用出现（[Env]），接自己的密钥库用 [WithSecrets]。
//   - 智能体：[WithAgent]、[WithAgents]、[Dir]、[WithDefaultAgent]。不给名字时的选法见 [App.NewSession]。
//   - 工具：[WithTools]。
//   - 其余：[WithWorkdir]、[WithUI]、[WithConfig]、[WithProtocolUI]、[WithSleepAfter]、[WithProjectTrust]、[WithKernel]。
//
// # 应用宿主选项
//
// 给像产品客户端那样自己管 profile、工作区和前端的程序用，只写应用不需要：
// [WithProfile]、[WithService]、[WithWorkspace]、[WithSessionHooks]、[WithModeApprovals]、[WithProtocolOptions]、
// [WithReadyErrors]。[App.Internals] 返回内核对象，不在稳定承诺里。
//
// # 会话
//
// [App.Ask] 新开会话问一句；[App.NewSession] / [App.Session] 拿到 [Session]，用 [Session.Send] 多轮、
// [Session.Stream] 逐个取 [Event]（正文、工具调用、确认、结束）。没有界面能回答时，需要确认的调用立即拒绝。
// 身份：[WithTenant]、[WithUser] 写进 ctx；业务数据放 [Meta]。
//
// # 工具
//
// [Func] 按参数结构体生成 JSON Schema，[Raw] 自己给 schema。修饰：[Tool.Dangerous]、[Tool.Writes]、
// [Tool.Timeout]、[Tool.Serial]。工具里用 [CallInfoFrom] 取会话、智能体、用户。
//
// # 写组件
//
// 组件实现 [Component]（Name / Setup / Start），可选再实现 Info() [ComponentInfo] 声明配置和服务依赖，用 [With] 加进应用。
// Setup 里：
//
//   - 点位：[RegisterAt] 在 [PointTools]（[ToolsOf]）、[PointSystemBlock]、[PointLayer]、[PointAfterTurn] 上登记，
//     登记项类型由点位决定，写错编译不过。
//   - 服务：[NewKey] 定义带类型的键，[Provide] 提供，[Get] / [GetOptional] 取用。
//   - 配置：Registrar.Config 取按 ComponentInfo.Config 解码好的结构体指针。
//   - 守卫：Registrar.Guard 登记 [GuardRule]，在 [GuardDeliver]、[GuardSessionOpen]、[GuardToolCall] 三处执行。
//
// Start 里用 Scope.Go 起后台工作、Scope.OnClose 登记清理。
//
// # 前端
//
// [App.API] 交给 tenon/transport/web（网页服务）或 tenon/transport/bind（桌面壳）；前端用 @tenon/web 或按应用协议契约自己写。
// 要让前端回答确认，New 时加 [WithProtocolUI]。
//
// # 错误
//
// [ErrNotStarted]、[ErrInvalid]、[ErrNotFound]、[ErrConflict]、[ErrDenied]，用 errors.Is 判断。
package tenon
