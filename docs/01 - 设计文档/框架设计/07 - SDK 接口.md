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
| 应用选项 | 存储 `WithStore`（框架只带 `runtime/store/memstore` 的 `MemoryStore`，SQLite 等持久化实现由产品提供，见 `docs/02 - 开发管理/20`）；模型 `WithOpenAI`、`WithOpenAIResponses`、`WithAnthropic`、`WithModel`、`WithModels`、`WithModelCatalog`、`WithSecrets`、`Env`；智能体 `WithAgent`、`WithAgents`、`Dir`、`WithDefaultAgent`；工具 `WithTools`；其余 `WithWorkdir`、`WithUI`、`WithConfig`、`WithProtocolUI`、`WithSleepAfter`、`WithProjectTrust`、`WithKernel` |
| 应用宿主选项 | `WithProfile`、`WithService`、`WithWorkspace`、`WithSessionHooks`、`WithModeApprovals`、`WithProtocolOptions`、`WithReadyErrors`；`App.Internals()`（不在承诺里，见上） |
| 会话 | `App.Ask` / `NewSession` / `Session` / `History`、`Session.Send` / `Stream` / `History` / `Cancel`、`Event` 与 `Event*`、`Reply`、`Meta`、`WithTenant`、`WithUser` |
| 工具 | `Func`、`Raw`、`SchemaOf`、`Tool` 的修饰方法、`CallInfoFrom` |
| 写组件 | `Component`、`ComponentInfo`、`Registrar`、`Scope`；点位 `Point`、`RegisterAt`、`PointTools` / `ToolsOf`、`PointSystemBlock`、`PointSessionInit`、`PointAfterTurn`；服务 `Key`、`NewKey`、`Provide`、`Get`、`GetOptional`；结论 `Verdict` / `Allow` / `Deny` / `Abstain`、点位 `GuardDeliver` / `GuardSessionOpen`、请求 `GuardRequest`。**注意：登记判定区用的 `WrapGuard` 与登记中间件用的 `Wrap` 目前没有在根包导出**（见 7.6 的现状差距），写组件要登记它们得 import `runtime/component`，而那个包不在承诺里 |
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

### 7.4.1 什么时候该发版（2026-11-13 补）

原版本规则只写了「版本号怎么定」，没写「什么时候该打 tag」，结果是 v0.1.2 之后
积压 133 个提交、公开面已有破坏性变更却没发版。补一节判据。

**发版的三个触发条件（满足任一就发）：**

1. **公开面有变化**。`testdata/api.txt` 与上一个 tag 有差异，无论增删。
   这是最主要的触发条件——下游只看得到 tag，看不到 `main`。
2. **有下游要用**。任何外部项目/产品要 `go get` 一个还没发过的行为时，
   先发版再让对方引用，不要让对方依赖伪版本（`v0.0.0-<时间>-<hash>`）。
3. **修了安全或正确性问题**。即使公开面没变也要发，让下游能拿到修复。

**不需要发版的情况**：纯文档、注释、测试、CI 调整；内部重构而公开面未变
（这类可以攒着，等下一次公开面变化一起发）。

**发版前必须逐条过（缺一不可）：**

| # | 检查 | 怎么验 |
|---|---|---|
| 1 | 工作区干净、本地与两个远程同步 | `git status --short` 空；`git rev-list --left-right --count origin/main...HEAD` 为 `0 0` |
| 2 | 全量测试与两套快照通过 | `go test ./...`、`apphost` 回归快照、`TestAPISnapshot` |
| 3 | `gofmt -l .` 与 `go vet ./...` 干净 | 同上 |
| 4 | 双仓库 CI 在待发提交上全绿 | GitHub Actions + Gitea 两个 job |
| 5 | **公开面差异已过目** | `git diff <上个tag>:testdata/api.txt testdata/api.txt`，逐条确认是有意改动 |
| 6 | **CHANGELOG 写好了** | 见下 |
| 7 | 版本号与改动性质匹配 | 见下表 |
| 8 | 打**附注 tag**（`git tag -a`，带一句说明），推两个远程 | `git push origin <tag> && git push gitea <tag>` |
| 9 | 发版后验证 tag 能被解析 | `GOPROXY=https://goproxy.cn go list -m -versions <模块路径>` 应列出新版本 |

**版本号怎么定（v0 期间）：**

| 改动性质 | 版本号 | 例子 |
|---|---|---|
| 只有修 bug / 内部重构，公开面不变 | patch（`v0.1.3`） | 修 flake、改注释 |
| **公开面有增删或签名变化** | **minor（`v0.2.0`）** | 删 `MemoryStore`、`GuardRule` 体系重构 |
| 只增不删（加选项、加方法、加字段） | patch 或 minor 均可，倾向 patch | 加 `WithLayeredConfig` |

> **注意**：v0 期间「接口可以改」说的是**允许改**，不是说**不用升 minor**。
> semver 的 `v0.x.y` 里 `x` 是 minor，破坏性变更该动 `x`。把破坏性变更塞进 patch 号
> 会让下游以为是「无痛升级」而直接 `go get -u`，升级后编译失败——这是最该避免的体验。

**一个已核实的坑（2026-11-13）**：`retract v0.1.0` 写在 `go.mod` 里，
按 Go 的规则（`go help modules`：取 `@latest` 版本的 `go.mod` 读 retract 列表），
它在 **v0.1.2 发布时就已生效**，不需要等新版本。实测：

```console
$ go list -m -versions github.com/Sevyn17/tenon
github.com/Sevyn17/tenon v0.1.1 v0.1.2          # v0.1.0 已被隐藏
$ go list -m -versions -retracted github.com/Sevyn17/tenon
github.com/Sevyn17/tenon v0.1.0 v0.1.1 v0.1.2   # 加 -retracted 才看得到
```

所以「retract 要等下一个版本才生效」是**误解**，不要拿它当发版理由。

## 7.5 守卫执行的位置

判定区（`WrapGuard`）登记在点位上，由内核在对应时机跑。**2026-11-12 的 H4 治理之后**
只剩两个点位由内核执行，且走的是 `WrapGuard` 判定区（不是旧的 `GuardRule`）：

| 位置 | 时机 | 被拒时 |
|---|---|---|
| `GuardDeliver` | 信封投递前 | 投递失败，记 `guard.denied` |
| `GuardSessionOpen` | 新建会话前 | `NewSession` / `Ask` 返回 `ErrDenied` |

工具调用与其余点位走 `component.WrapGuard(cx, component.ToolCall, ...)` 等判定区，
由 `Assembly.EvalPoint` 合并结论；`GuardTurn` / `GuardModelCall` 已随 H4 删除（零调用点）。

合并规则：任一条 Deny 即拒绝；全部 Abstain 走内核缺省；Allow 只放开"往 `session:` 投递"的缺省限制。

> **历史说明**：本节原写「`Registrar.Guard` 登记的规则在三处执行」并列出 `GuardToolCall`。
> 那套 `GuardRule` / `Registrar.Guard` 机制在 2026-11-12 的 H4 治理中**整套删除**——
> 核查发现它在生产代码里零登记、整套空转（详见 17 篇第二节 H4）。现在统一走判定区。

## 7.6 现状差距

| 现状 | 目标 | 处理 |
|---|---|---|
| **`WrapGuard` / `Wrap` 未在根包导出**（2026-11-13 发现）。登记判定区与中间件必须 import `runtime/component`，而该包不在承诺里；框架自己的 `examples/component` 就是这么写的（注释里也注明了） | 写组件只 import `tenon` 一个包 | **待做**。H4/L1 重构把 `Registrar.Guard` 换成 `WrapGuard` 时，只在 `runtime/component` 落地、没在根包补导出与别名。补法：根包加 `WrapGuard` / `Wrap` 的薄包装 + 六个 `MwPoint` 别名 + 对应 `*Req` / `*Resp` 别名，然后重录快照 |
| `Send(to, body)` 发信封、`Events(after)` 订阅、`Respond(corr)` 回答 | 应用用信封与地址和其他会话通信 | 未做。现在由 `Session.Send` / `Stream` 和应用协议覆盖；有外部需求再加到根包 |
| `Fork` / `Rewind` / `Migrate` / `SetMeta` | 会话操作 | 应用协议里有对应命令，根包未暴露 |
| 多租户鉴权（`WithAuthenticator`） | 服务按请求取租户和用户 | v1 后（T4）；现在 `transport/web` 只有令牌 + Host / Origin 检查 |
| 守卫只在投递、建会话、工具调用三处执行 | 回合与模型调用也能拦 | `GuardTurn` / `GuardModelCall` 已在 2026-11-12 的 H4 治理中删除（零调用点）。有需要时再在 agent 里加执行点 |

## 7.7 框架负责什么、应用负责什么

框架是库不是服务：它提供机制，不替应用做部署决策。安全与数据两类责任最容易混淆，
所以各列一张清单。

### 安全

**框架负责**：

| 项 | 实现位置 |
|---|---|
| SSRF 拦截：`web` 工具在**实际连接的 IP** 上校验，不在 URL 上校验（DNS 重绑定绕不过） | `modules/tools/web/web.go` 的 `publicAddr` + `Dialer.Control` |
| 路径穿越防护：`fs` 工具按字面路径与**真实路径**双重检查（防 junction / symlink 逃逸） | `modules/tools/fs/tools.go` 的 `resolve` |
| 命令执行不经 shell 拼接：`exec.CommandContext` 传参，`-Command` 由 argv 传 | `runtime/host` 与 `modules/internal/proc` |
| SQL 全参数化；动态标识符经 `quote()` 转义 | 存储实现（已迁出框架，见 20 篇）；框架的 `store.SQL` 只给组件自有表用 |
| 沙箱 **fail-closed**：无法强制执行时返回 `ErrUnavailable`，调用方必须拒绝，不降级 | `modules/security/sandbox` |
| 权限默认 `AskUser`，不是默认放行 | `modules/security/permission` |
| 不可信输入不跨会话「洗白」：父回合不可信则子会话按不可信投递 | `Host.SpawnAs` 的 `untrusted` |

**应用负责**（框架不做）：

| 项 | 为什么框架不做 |
|---|---|
| 传输加密（TLS）、证书管理 | 部署层决定，框架不持有证书 |
| 存储加密 | 库文件由应用保管，密钥管理方案属应用 |
| PII 清单、数据分级、留存期限、跨境评估 | 框架不知道业务数据含义与合规要求 |
| 身份认证与会话鉴权（多租户） | v1 后（T4）；现在 `transport/web` 只有令牌与 Host / Origin 检查 |
| 审计留存的期限与导出 | 框架记录事件，怎么留、留多久由应用定 |

### 数据与备份

**框架负责**：会话数据以标准 SQLite 文件存储，`export` 导出 JSONL 供**排查与轨迹投影**。

**应用负责**：备份与恢复。框架不做，原因是存哪由应用决定（见 18 方案），
备份策略要跟着存储实现走。

> **`export` 的 JSONL 不可反向导入。** `Import` 是库对库并表（`ATTACH` 后按列名 `INSERT`），
> 与 `Export` 的 JSONL 不是一对格式。要恢复整库请用下面的 SQLite 方法。

**备份会话库的正确做法**：

```sql
-- 方式一（推荐）：生成一致的快照文件，不阻塞写
VACUUM INTO '/path/to/backup.db';

-- 方式二：先合并 WAL 再拷文件
PRAGMA wal_checkpoint(TRUNCATE);
-- 然后拷 state.db（此时 -wal 已空）
```

**不要直接拷 `state.db`**：库用 WAL 模式（`journal_mode=WAL`），
未合并的数据在 `state.db-wal` 里。只拷 `.db` 会拿到一个缺最近提交的旧快照，
而且看起来「能打开」，问题要等恢复后才发现。

**恢复**：停进程 → 用备份文件替换 `state.db` → 删掉残留的 `state.db-wal` / `state.db-shm`
→ 启动。启动时迁移会校验 schema 版本，版本过高会拒绝打开（不会静默改坏数据）。

**写入是串行的**：SQLite 单写者语义，存储实现用单写连接
（`SetMaxOpenConns(1)`）+ 4 个只读连接。高并发写会排队，这是 SQLite 的固有特性，
不是缺陷；需要更高写吞吐时换存储实现。实现已迁出框架（见 20 篇）。

