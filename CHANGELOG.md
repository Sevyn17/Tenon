# 变更日志

本文件记录**已发布版本**与**已合入但未发版**的变更。版本规则与发版判据见
[07 - SDK 接口](<docs/01 - 设计文档/框架设计/07 - SDK 接口.md>) 7.4 与 7.4.1。

格式：`## [版本] - 日期`，小节用「新增 / 变更 / 修复 / 破坏性变更」。
**破坏性变更单独成节并置顶**——v0 期间公开面允许改，但必须让下游一眼看到。

---

## [v0.2.0] - 2026-11-13

> 本版含**破坏性变更**（公开面 10 处删除），按 semver 动 minor。
> 从 `v0.1.2` 起 138 个提交；破坏性变更清单见下节，逐条都有替代方案。

### 破坏性变更

- **根包删 `MemoryStore()` / `SQLiteStore(path)`**。存储实现改为按需引入：
  `runtime/store/memstore.MemoryStore()`、`modules/storage/sqlite.SQLiteStore(ctx, path)`。
  理由：`import tenon` 不该把 SQLite 驱动编进程序（内核与 I/O 分离第 1 步）。
  注意新签名多一个 `ctx`，不再内部用 `context.Background()`。
- **删守卫规则体系**：`tenon.GuardRule`、`tenon.GuardToolCall`、`Registrar.Guard`。
  核查发现该机制在生产代码里**零登记、整套空转**（`GuardRules()` 恒空 →
  `evalGuard` 恒 `Abstain` → `toolGuard` 恒 `false`），已并入 `WrapGuard` 判定区。
  替代：组件在 `Setup` 里用 `component.WrapGuard(cx, 点位, 名字, 判定函数)` 登记。
- **`GuardRequest` 字段 7 → 3**：只剩 `Point`、`Envelope`、`Agent`。
  删掉的 `Tool` / `Args` / `Risk` / `Model` 在剩余两个点位（`GuardDeliver`、
  `GuardSessionOpen`）上都没有读取方。
- **删 `LayerEntry`、`tenon.PointLayer`、`ModelHub.AddLayer`**。会话层已拆掉，
  四类扩展各归其位：贡献走 `RegisterAt`，拦截走 `Wrap`，决策走 `WrapGuard`，
  通知走 `Ready` / `AfterTurn` / `Delete`。会话构造后的初始化改走 `PointSessionInit`。

### 新增（协议与前端）

- **`appapi/wire.ModeInfo` 加 `Short` 字段**（权限档位简称）：工具栏与档位菜单显示简称，
  完整标签放悬停提示与菜单描述。前端同步（05 篇 F14）。这是加字段，不升协议号。

### 新增

- **T4b–T4f**：持久事件、资源与订阅、插件包带定义、小组（群聊）模块、
  多人会话与权限收尾（hop 可配、control 触发者放行、按人分轮、预算记账与挂起、
  有效权限含租户/用户上限并按本轮触发者重解）。
- **T5a–T5c**：常驻定义（`resident`）、定时与外部事件入口（`events` 组件）、
  模型判断类匹配器（`ModelMatcher` + `modelmatch` 组件）。
- **T6a–T6b**：`remote:` 地址与 Agent Card、HTTP 出站客户端与入站 handler、
  跨机器事件续读；扩展进程 JSON-RPC 2.0 协议、崩溃退避重启、stdio 回桥、
  工具/匹配器/资源类型/渲染器桥接。
- **04 4.3 六个中间件点位全部落地**：Deliver、ToolCall、SessionOpen、Turn、
  Context、ModelCall，统一用 `Middleware[Req,Resp]` + `MwPoint` + `Wrap` + `Chain`。
- **判定区与决策服务原语**：`WrapGuard`（纯谓词并行 reduce）+ `Authorizer`。
- **`tenon.WithLayeredConfig`**：部署期配置来源抽象。应用可从数据库或远程提供配置，
  也能只用 Go 值、零文件运行。文件实现在 `adapters/configfile`，不进内核。
- **`tenon.WithPackages`**、`transport/web` 的 `RemoteClient` / `RemoteHandler` /
  `RemoteEventsHandler`。

### 变更

- **内核与 I/O 分离六步全部落地**：`runtime/` 不再引用 `net/http`、`modernc.org/sqlite`；
  `import tenon` 不再带入 SQLite 驱动。允许名单从 18 条缩到 7 条，每条都有理由。
- **SQLite 实现搬到 `modules/storage/sqlite`**（不是 `adapters/`：标准模块不许引用
  `adapters/`，照搬会让依赖规则测试失败）。
- **`agentdef.DirSource` 改吃 `fs.FS`**：目录由调用方给，内核不再自己扫。
- **profile 读写搬到 `adapters/profile`**；插件包扫描搬到 `adapters/plugins`。
- **嵌入场景缺省不再读 `~/.tenon/hooks.toml` 与 `~/.tenon/permissions.toml`**
  （有意的行为变化）：嵌进别的软件的智能体不该悄悄吃掉开发者个人目录的配置。
  要文件配置的嵌入方显式传 `WithLayeredConfig`；`apphost` 自己接上，独立运行行为不变。
- **`hooks` 的 `Setup` 不再自己拼路径、判断受信**，改从配置层取；脱敏跟来源走。
- **`Turn.Resumed` 改为显式传参**，不再读 `ag.Pending()` 的时序副作用。
- **`Host` 包拆分**：`host.go` 1369 → 414 行，actor 独立成文件。

### 修复

- **`group` 组件在「存储由插件提供」时启动失败**：`Setup` 里就取 `DBHandle.DB()`，
  而该句柄在 `Start` 之前返回 nil，于是 `Start` 里 `New(...)` 报「缺少依赖」。
  走 `apphost` 必然踩到。改为 `Setup` 只存句柄、`Start` 里取，与 `workgroup` 对齐。
- **`WrapGuard` 类型断言注释说假话**：原注释称「点位与 Req 类型不符是编译错误」，
  实际能编译、能生效、只是拿到零值 req（比不判更危险）。改为如实说明 + 类型不符
  退化为 `Abstain`。
- **删掉空转的延迟显露机制**：`Deferred` / `tool_search` / `Catalog` / `TakePending` /
  `ConfigPatch.AddTools` 整条链生产不可达（预算恒 0），设计依据也只在归档里。
  按「能删就删」整套删除，净减约 100 行。
- 修 `TestFanoutAcceptance` 的异步事件落库竞态。
- 修 `TestShellBackgroundJob` 的脆弱点（不假定 job 编号、给启动留余量）。
- **`Host` 关闭后不再起后台派发**（`Host.Close()`，`tenon.App.Close()` 已接上）：
  派发协程用 `context.Background()`，不受调用方 context 约束，关存储后仍会访问数据库。
  CI 上偶发的临时目录清理失败（`directory not empty`）就是它。
- **`shell` 的 `WaitDelay` 2 秒 → 10 秒**：进程退出但子进程占着管道时，2 秒不够读完尾部输出，
  表现为「已结束但输出不全」。
- **`job_output` 增量读不再吞掉残缺 ANSI 序列后的文本**：切点落在转义序列中间时，
  原实现会把后续正常文本当成序列参数丢掉；改为回退读偏移、等序列闭合。
- **交互登记与等待者挂载顺序**：原先先落库、后挂等待通道，中间窗口里到达的回答会把交互
  误判成「上次进程留下的」并标成 `voided`。改为落库前挂通道。

### 内部（不影响使用方）

- 结构性问题治理两轮：18 项已修、1 项撤项、5 项明确不做。详见
  [17 - 结构性问题治理方案](<docs/02 - 开发管理/17 - 结构性问题扫描.md>)。
- 质量五项：删重复工具函数、补 17 个包级注释、拆 `tenon.go` 的 `build`、
  补 73 个导出符号的文档注释、拆 `host` 包。
- 测试支持 `-count>1`（恢复「连跑多次查 flake」的能力）。

---

## [v0.1.2] - 2026-11-09

### 新增

- **应用身份可设**：`tenon.SetAppID(AppID)` / `tenon.CurrentAppID()`，
  统一程序名、数据目录与环境变量前缀（原来散在 `appid` 包里各写各的）。

---

## [v0.1.1] - 2026-11-09

### 变更

- CI 增加文件名检查：仓库里的文件名只用 Go 模块接受的字符。

### 说明

- 这是**首个可用版本**。`v0.1.0` 因文档文件名含全角标点，Go 模块 zip 不接受，
  `go get` 下载不了，已作废并在 `go.mod` 里 `retract`。

---

## [v0.1.0] - 2026-11-09（已作废）

### 说明

- 框架独立成仓库的第一个 tag：模块路径改为 `github.com/Sevyn17/tenon`，
  `git filter-repo` 把 `framework/` 提到仓库根。
- **已 `retract`**：文档文件名含全角标点导致 `go get` 失败。不要使用。

---

## 说明

- **v0 期间**：公开面可以改，但每次改都要过快照（`testdata/api.txt`）、写决策日志，
  并在本文件里记「破坏性变更」。v0 不是「可以随便改」的意思——下游看得到的是 tag。
- **v1 起**：小版本只加不删、不改含义；不兼容改动只在大版本，大版本换模块路径（`…/v2`）。
- 详细规则见 [07 - SDK 接口](<docs/01 - 设计文档/框架设计/07 - SDK 接口.md>) 7.4 / 7.4.1。
- 每个版本的完整提交列表：`git log <上个tag>..<tag> --oneline`。
