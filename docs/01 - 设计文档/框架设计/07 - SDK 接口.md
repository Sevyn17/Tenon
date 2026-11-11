# 07 SDK 接口：公开面清单

> 状态：定稿（2026-11-09，16 - SDK 对外可用性方案阶段 B，原计划/08）。列出哪些包、哪些名字在稳定承诺里，以及它们怎样演进。
> 怎么用见 [上手](<../../00 - 上手指南/01 - 十分钟 - 带工具的智能体.md>)；完整签名以 `testdata/api.txt` 为准。
> 2026-11-01 的草案（`Send(to, body)`、`Events`、`Respond`、`Fork` 等设想）见本文件的 git 历史，未实现的部分列在 7.6。

## 7.1 承诺的包

模块路径是 `github.com/Sevyn17/tenon`（2026-11-09 定，16 方案阶段 C，原计划/08）。下表的 `tenon/…` 是简写，指 `github.com/Sevyn17/tenon/…`；根包的包名是 `tenon`。

| 包 | 给谁用 | 内容 |
|---|---|---|
| `tenon`（根包） | 应用 | `New` 与选项、`App`、`Session`、工具外观、写组件用的点位 / 服务键 / 守卫、错误值 |
| `tenon/tenontest` | 应用的测试 | 脚本模型（`Model`、`Reply`、`CallTool`）、`New` / `NewOn` / `Stores`、`AssertToolCalled` |
| `tenon/transport/web` | 接前端 | HTTP + WebSocket 服务（`Config`、`New`、`NewToken`、`LoopbackHosts`、`WriteTokenFile`） |
| `tenon/transport/bind` | 接桌面壳 | 把应用协议绑到进程内调用（`NewService`） |
| `tenon/appapi/wire` | 前端与协议客户端 | 应用协议的帧、命令参数与事件结构（契约见 [06 - 应用协议契约](<../06 - 应用协议契约.md>)） |
| `tenon/apphost` | 建在 SDK 上的应用宿主 | 读 profile、项目信任、工作区切换、`Start` |
| `tenon/adapters` | 应用宿主 | 把内核对象接到应用协议的适配（`Fill`、`Parts`、模式批准） |

`tenontest` 之外的四个包给"应用宿主"用（像产品客户端那样自己管 profile、工作区和前端的程序）；只写应用不需要它们。

### 不在承诺里

- `tenon/runtime/*`：内核。可能改名、拆分、换签名。
- `tenon/modules/*`：内置组件的实现。组件名和配置键见 [04 - 核心与扩展模块清单](<../04 - 核心与扩展模块清单.md>)，Go 代码可能变。
- `tenon/appapi`：应用协议的服务端实现。前端只依赖 `appapi/wire` 与协议契约；`App.API()` 返回的 `*appapi.App` 只拿来交给 `transport/*`。
- `App.Internals()` 返回的内核对象（Host、Bus、Assembly、Models、Definitions、DB、Blobs、Builder）：给应用宿主用，字段和类型跟着内核变。
- 根包里指向 `runtime/*` 的别名（`Registrar`、`Scope`、`SessionCx`、`ToolsEntry`……）：别名本身在承诺里，快照连同目标类型的导出字段一起记录；目标里没被别名带出来的类型（如 `SessionCx.Hub` 的 `*toolhub.Hub`）不在承诺里。

## 7.2 根包分组

根包 godoc 按下面的分组写成目录页。

| 组 | 名字 |
|---|---|
| 建应用 | `New`、`App.Start` / `Open` / `Close`、`Default`、`Minimal`、`With`、`Without` |
| 应用选项 | 存储 `WithStore`、`MemoryStore`、`SQLiteStore`；模型 `WithOpenAI`、`WithOpenAIResponses`、`WithAnthropic`、`WithModel`、`WithModels`、`WithModelCatalog`、`WithSecrets`、`Env`；智能体 `WithAgent`、`WithAgents`、`Dir`、`WithDefaultAgent`；工具 `WithTools`；其余 `WithWorkdir`、`WithUI`、`WithConfig`、`WithProtocolUI`、`WithSleepAfter`、`WithProjectTrust`、`WithKernel` |
| 应用宿主选项 | `WithProfile`、`WithService`、`WithWorkspace`、`WithSessionHooks`、`WithModeApprovals`、`WithProtocolOptions`、`WithReadyErrors`；`App.Internals()`（不在承诺里，见上） |
| 会话 | `App.Ask` / `NewSession` / `Session` / `History`、`Session.Send` / `Stream` / `History` / `Cancel`、`Event` 与 `Event*`、`Reply`、`Meta`、`WithTenant`、`WithUser` |
| 工具 | `Func`、`Raw`、`SchemaOf`、`Tool` 的修饰方法、`CallInfoFrom` |
| 写组件 | `Component`、`ComponentInfo`、`Registrar`、`Scope`；点位 `Point`、`RegisterAt`、`PointTools` / `ToolsOf`、`PointSystemBlock`、`PointLayer`、`PointAfterTurn`；服务 `Key`、`NewKey`、`Provide`、`Get`、`GetOptional`；守卫 `GuardRule`、`GuardDeliver` / `GuardSessionOpen` / `GuardToolCall`、`Allow` / `Deny` / `Abstain` |
| 前端 | `App.API()`，交给 `transport/web` 或 `transport/bind` |
| 错误 | `ErrNotStarted`、`ErrInvalid`、`ErrNotFound`、`ErrConflict`、`ErrDenied`，用 `errors.Is` 判断 |

## 7.3 快照

`api_snapshot_test.go` 用 `go list -export` 取承诺包的编译导出数据，`go/importer` 读出类型，把全部导出名与签名写进 `testdata/api.txt`（每行一项，按包、按名排序）。不依赖要联网安装的工具，约 3 秒。

- 不一致时测试失败，列出多出和少掉的行，提示"公开接口变了：更新快照并写决策日志"。
- 有意改公开面：`go test -run TestAPISnapshot -update-api .` 重录，同一个提交里在 [决策日志](<../../02 - 开发管理/02 - 决策日志.md>) 写一行：改了什么、为什么。
- `-short` 下跳过。
- 承诺的包有变动时同步改测试里的 `promised` 列表和本篇 7.1。

## 7.4 版本规则

- **标签**：框架在仓库根，标签不带前缀（`v0.1.1`、`v0.2.0`…）。`v0.1.0` 因文档文件名含全角标点无法 `go get`，作废（见决策日志 2026-11-09）；仓库里的文件名只用 Go 模块接受的字符，`scripts/ci.sh` 会检查。
- **v0（现在）**：接口可以改，但每次改都要过快照、写决策日志。能加的不改：加选项、加方法、加字段优先于改签名。
- **v1 起**：小版本只加不删、不改含义；不兼容改动只在大版本，大版本换模块路径（`…/v2`）。
- **弃用**：标 `// Deprecated:` 并写替代方式，至少保留两个小版本，在下一个大版本删除。
- 应用协议（`appapi/wire`）有自己的 `protocol` 号（06 应用协议契约）：加命令、加事件、加字段不升号；改语义或删字段升号。
- 定义包格式有自己的版本（03 3.4）：新 SDK 读旧格式必须兼容，读到不认识的新格式直接拒绝。
- `meta` 与 `x.*` 的内容框架原样保存，不解释。

## 7.5 守卫执行的位置

`Registrar.Guard` 登记的规则在这三处执行：

| 位置 | 时机 | 被拒时 |
|---|---|---|
| `GuardDeliver` | 信封投递前 | 投递失败，记 `guard.denied` |
| `GuardSessionOpen` | 新建会话前 | `NewSession` / `Ask` 返回 `ErrDenied` |
| `GuardToolCall` | 工具执行前（先于权限组件的授权层） | 失败结果交给模型，这一轮继续 |

合并规则：任一条 Deny 即拒绝；全部 Abstain 走内核缺省；Allow 只放开"往 `session:` 投递"的缺省限制。`runtime/component` 里还有 `GuardTurn`、`GuardModelCall` 两个位置，内核没有执行它们，所以根包不导出。

## 7.6 现状差距

| 现状 | 目标 | 处理 |
|---|---|---|
| `Send(to, body)` 发信封、`Events(after)` 订阅、`Respond(corr)` 回答 | 应用用信封与地址和其他会话通信 | 未做。现在由 `Session.Send` / `Stream` 和应用协议覆盖；有外部需求再加到根包 |
| `Fork` / `Rewind` / `Migrate` / `SetMeta` | 会话操作 | 应用协议里有对应命令，根包未暴露 |
| 多租户鉴权（`WithAuthenticator`） | 服务按请求取租户和用户 | v1 后（T4）；现在 `transport/web` 只有令牌 + Host / Origin 检查 |
| 守卫只在投递、建会话、工具调用三处执行 | 回合与模型调用也能拦 | 有需要时在 agent 里加执行点，再导出 `GuardTurn` / `GuardModelCall` |
