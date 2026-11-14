# 变更日志

本文件记录**已发布版本**与**已合入但未发版**的变更。版本规则与发版判据见
[07 - SDK 接口](<docs/01 - 设计文档/框架设计/07 - SDK 接口.md>) 7.4 与 7.4.1。

格式：`## [版本] - 日期`，小节用「新增 / 变更 / 修复 / 破坏性变更」。
**破坏性变更单独成节并置顶**——v0 期间公开面允许改，但必须让下游一眼看到。

---

## [v0.3.2] - 2026-11-14

> 补丁：公开面**新增** 5 个符号（`wire.DeclNode` / `DeclAction` / `DeclNodeTypes` /
> `DeclActionKinds` / `ValidateView`），无删除、无签名变更。修 `[[ui]]` 形状契约、
> 把它的定义收归一处、并补上协议边界校验。

### 修复

- **`[[ui]]` 声明式视图契约的四处字段名与渲染器不一致**。初版（`7c48b50`）是渲染器
  迁出之后凭描述转写的，写成了 `code.text`、`keyValue.keyValue`、`button.text` /
  `link.text`；应用侧渲染器（Mortise `client/web/src/ui/declarative.tsx`）实际读的是
  `code`、`rows`、`label`。照契约写出的插件视图在应用里会渲染成**空代码块、无文字的按钮**。
  这类错误此前无法被发现：`UISlot.View` 是 `json.RawMessage`，框架只校验大小与顶层
  `type`，`DeclNode` 在框架里没有任何反序列化点。现在契约按渲染器实现逐字段对齐，
  并写清三处「同一 JSON 字段两种语义」（`label` / `rows` / `value`）；
  `badge` 的 tone 词表（`neutral/accent/success/danger/warning`）与 `text` 的
  （`default/muted/danger/success`）分开写明；`button.variant` 补上 `ghost` / `outline`。

### 变更（架构：契约收归一处，与框架既有做法一致）

- **声明式视图的形状从 `runtime/pkgs` 移到 `appapi/wire/declarative.go`**。
  原先 `pkgs` 里有一份 `DeclNode`，而框架里**所有**线上类型（`Entry`、`UISlot`……）
  都由 `appapi/wire` 定义、经 `tsgen` 生成到 `web/src/wire.gen.ts`——视图是
  `UISlot.View` 的载荷，属于线上类型，之前是唯一的例外。这个例外正是错误的来源：
  `pkgs` 属 `runtime/`，依赖规则禁止它 import `appapi`，所以它只能**复制**一份形状，
  而复制品没有任何手段与权威定义校验。
  现在：
  - 形状只在 `wire` 定义一处，一张 `declKinds` 表同时驱动**校验、TS 类型生成、文档**；
  - `pkgs` 只做清单层校验（大小、顶层 `type`、id/slot、`data.cmd` 只读），不再知道节点形状；
  - `runtime/pkgs/contract_ownership_test.go` 钉住「本包不得再定义这些类型」，
    防止有人重新复制一份（变异验证：加回 `type DeclNode struct` 立刻红）。
- **协议边界补上视图校验**：`adapters/plugins` 加载插件包时调 `wire.ValidateView`，
  按 `declKinds` 检查必填字段、`action.kind`、递归子节点。未知 `type` **不报错**
  （渲染器显示占位，前向兼容优先）。此前这类错误要等到界面上渲染成空白才被发现。
- **TS 类型由同一张表生成**：`wire.gen.ts` 新增 `DeclNode` / `DeclAction` 可辨识联合，
  `UISlot.view` 从 `unknown` 收窄为 `DeclNode | DeclNode[]`（Go 侧仍是 `json.RawMessage`：
  视图可能是单节点或数组，且要原样透传给前端做占位替换）。

### 新增

- **`appapi/entry_boundary_test.go`**：入口无关约束测试（19 篇第五节承诺的那条）。
  三条规则：`appapi` 与 `appapi/wire` 不得 import 外壳与装配层；非测试源码不得出现
  `cli` / `desktop` / `webhost` / `tui` / `gui` 字面量；不得读 `os.Args`。
  核心不知道自己在被谁用，这条线要靠测试守，不能靠自觉。
- **`appapi/wire/declarative_test.go`**：契约护栏。表引用的字段必须真的在 `DeclNode` 上；
  校验器按表工作（必填缺失报错、未知节点放行、历史错误写法要拒）；生成的 TS 与表一致。
- **`runtime/pkgs/contract_ownership_test.go`**：界面契约只在 `appapi/wire` 定义一处。

### 说明

- `DeclNodeTypes()` / `DeclActionKinds()` 是函数不是 `var`：返回副本，调用方改不动契约。
- 应用侧（Mortise `client/web`）因 L2 层不得 import 协议客户端，保留一份结构等价的
  `DeclNode` 声明，并用**编译期双向可赋值断言**（`src/shell/declarative-conformance.ts`）
  与生成的类型绑定：任一边漂移，`tsc` 立刻失败。

---

## [v0.3.1] - 2026-11-13（**未单独发布**，内容并入 `v0.3.2`）

> 补丁：公开面不变，修 v0.3.0 的一个正确性缺陷。从 `v0.3.0` 起。
>
> **2026-11-14 注**：这一段写进 CHANGELOG 后没有单独打 tag，修复提交 `6708e0c`
> 直接进了 `main`，随后与 `v0.3.2` 的内容一起发布。
> 所以 `v0.3.1` 这个版本号**不存在于 tag 列表里**——想拿这个修复请用 `v0.3.2` 及以上。
> 保留这一段是为了说明「`v0.3.0` 有个缺陷、`6708e0c` 修了它」这段历史。

### 修复

- **旧库并入的类型断言打回存储句柄**。v0.3.0 在 `DB()` 的返回值（`store.Store`）上
  断言 `LegacyImporter`，而实现方把 `Import` / `Path` 写在登记为 `service:db` 的句柄上。
  断言失败被当成「存储插件不支持并入」静默跳过，项目里的旧会话库从不并入用户级库。
  现在断言打在句柄上；`apphost/legacy_test.go` 用假句柄钉住断言位置，打错位置测试会失败。

---

## [v0.3.0] - 2026-11-13

> 小版本：公开面有删除，按 07 篇 7.4.1 动 minor。从 `v0.2.2` 起。

### 破坏性变更

- **删除 `modules/storage/sqlite` 与 `modules/storage/store-sqlite` 两个包**。
  SQLite 实现迁出框架，由产品在自己的仓库里提供存储插件并在入口包空导入。
  框架 `go.mod` 不再依赖 `modernc.org/sqlite`。
- **`apphost.Start` 不再自带存储插件**。按 profile 装配时，调用方必须自己登记一个
  提供 `service:db` 的组件，否则报「装配: 没有提供 service:db 的存储组件」。
  SDK 路径不受影响：`tenon.WithStore` 照旧注入。
- **`tenontest.Stores()` 只返回内存实现**。用它跑多后端验收的测试少一个后端。

### 新增

- **`apphost.LegacyImporter` / `LegacyImportResult`**：旧库并入改为接口，
  实现由产品的存储插件提供。`apphost` 不再直接打开 SQLite 文件。

### 变更

- 框架测试全部改用 `memstore`。`workgroup` 旧表搬迁的两处测试改用标准库
  `database/sql` 加自注册内存驱动，不再依赖 SQLite 驱动。
- `runtime/assemble/deps_test.go` 删掉为 SQLite 开的两个例外。
- `examples/web-server` 改用内存存储（示例不需要持久化）。

### 修复

- **`appapi` 的跨进程轮询间隔不再是包级变量**。原先 `PollInterval` 被测试改写时，
  上一个测试还没退出的订阅协程仍在读它，`-race` 下 `TestConnPollsOtherProcess` 失败。
  轮询间隔与背压阈值改为连接自己的字段，生产缺省值不变。

---

## [v0.2.2] - 2026-11-13

> 补丁版：**Go 侧公开面不变**（`testdata/api.txt` 无差异）。标准组件不再依赖 SQL 出口。

### 变更

- **`workgroup` 投递账改走集合**：`p_workgroup_runs` 改为集合 `workgroup.runs`
  （固定分区 `runs`、索引 `state`、成员 id 作 key）。`workgroup` 不再声明
  `Requires{SQL: true}`，`service:sql` 降为可选依赖，只用于把 v0.2.x 已发布库里的
  旧表搬进集合后删除；存储没有 SQL 出口（如内存实现）时跳过导入，不阻塞装配。
- **SDK 不再因存储没有 SQL 出口而悄悄去掉 `workgroup`**（`tenon.go` 的
  `resolveComponents`）。任何实现了 `store.Store` 的存储都能装起全部标准插件，
  不必再实现 `DB() *sql.DB`。

### 修复

- 无。

---

## [v0.2.1] - 2026-11-13

> 补丁版：**Go 侧公开面不变**（`testdata/api.txt` 无差异），只修文案、补契约、调整前端分层。
> 从 `v0.2.0` 起 13 个提交。
>
> **前端有破坏性变更**：包名 `@tenon/web` → `@tenon/client`，界面代码移出框架。
> 前端不在 Go 的公开面承诺里（07 7.1 以 `testdata/api.txt` 为准），所以按 patch 发；
> 但用前端的产品必须同步改依赖名与 import。

### 破坏性变更（前端）

- **包名 `@tenon/web` → `@tenon/client`**：框架不再提供界面代码，只留协议客户端。
  - 框架侧 16981 行 → 2074 行；L1 hooks / L2 组件库 / L3 工作台**移到产品仓库**。
  - `exports` 从 5 个入口收成 1 个：只剩 `"."`（原 `./client` 的内容，且不再有 `client/` 子目录）。
  - 删掉只给应用用的 `web/embed.go`、`web/vite.config.ts`、`web/index.html`、`web/public`、
    `web/scripts`。`examples/web-server` 的 `web.Config.Static` 改为不传（界面由应用提供）。
  - 为什么：界面（布局、组件、快捷键）是产品决策，放在框架里是负担；但协议客户端
    是 `appapi/wire` 的运行时对应物，留着才能验证「快照 + 游标订阅能还原状态」。
    详见 `web/README.md`。

### 新增

- **`runtime/pkgs/declarative.go`**：`[[ui]]` 段声明式视图的形状契约——`DeclNode`
  （15 种节点：stack/row/text/markdown/badge/code/diff/list/table/keyValue/progress/
  button/link/form/divider）与 `DeclAction`（4 种动作：call/openPanel/insert/respond）。
  原先 `UISlot.View` 是 `json.RawMessage`，只校验大小不校验形状；前端迁出后形状只在
  产品仓库的渲染器里，框架的扩展点会变成「有定义、无约束」。补这份定义让第三方插件
  与渲染器实现者都有据可依。

### 修复

- **信任提示里的目录名硬编码**：`apphost/trust.go` 写死「项目无 **.tenon** 配置」，
  而产品（如 mortise）的目录是 `.mortise`，用户按提示找不到目录。改用 `appid.DirName()`。
- **子会话继承父会话的 trace**：`spawn`（子智能体、工作组成员）投任务时带上父会话的 trace，
  `explain <trace>` 现在能看到一次任务经过的所有子会话。实现上 `session_inbox` 加 `trace` 列
  （迁移 v16，有默认值），所以**跨进程重启后链路仍然完整**。普通 `Send` 行为不变
  （同一会话连发两条消息仍是两个独立任务，不共用 trace）。

### 文档

- 新增 `02 - 开发管理/19 - 框架侧交付与验收交接`：对产品仓库的交接记录。
- `00 - 索引` 开头加「框架与产品」分工说明，避免两个仓库的文档混看。
- `web/README.md` 重写：说明为什么框架只留协议客户端。
- 修 9 处失效路径引用（`runtime/store/sqlite/*` 等第 3 步搬家后留下的）、
  `StampOutbound` 注释指向不存在的函数。

### 内部（不影响使用方）

- `web/` 的 `pnpm-lock.yaml` 重新生成（旧 lock 还是 16981 行时期的，含已删的 React / vite）。
- `web/src/client/*` 上移到 `web/src/`，去掉无意义的 `client/` 子目录。

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
