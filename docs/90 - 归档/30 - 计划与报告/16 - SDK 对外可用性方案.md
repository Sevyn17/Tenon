# 16 SDK 对外可用性方案

> 状态：方案已确认（2026-11-08），第五节三条按建议定；阶段 A 已完成（2026-11-08），阶段 B 已完成（2026-11-09），阶段 C 已完成（2026-11-09，模块路径 `github.com/Sevyn17/tenon`，首个可用版本 `v0.1.1`；`v0.1.0` 因文件名含全角标点无法 `go get`，作废）。前提是计划/07 已完成：只有一条装配路径，SDK 能接协议与前端。2026-11-10 从计划/移入归档并重编号（原计划/08）。
>
> 模块路径没用自有域名：自有域名带端口，Go 模块路径不允许端口，改用 GitHub 仓库路径（见决策日志 2026-11-09）。

## 一、目标

外部开发者不读框架源码，也能用它写自己的智能体程序。验收标准：

1. 在一个空目录里 `go get` 就能拿到框架，不需要 `replace` 本地路径。
2. 常规应用只 import `tenon` 一个包（测试另加 `tenon/tenontest`）：定义智能体、加工具、接真实模型、多轮对话、流式输出，都不碰 `tenon/runtime/...`。
3. 三十行左右跑通一个接真实模型、带一个自定义工具的程序。
4. 有一份照着做就能跑通的上手文档；示例代码在 CI 里编译，不会过期。
5. 公开面有清单和快照测试：改了公开接口，测试会提醒写进决策日志。

## 二、现状（2026-11-08 实测）

在仓库外建了一个独立模块：带一个查订单的工具组件，定义一个只能用这个工具的客服智能体，用脚本模型跑一轮。能跑通（用户 → 助手调工具 → 工具结果 → 助手回答），但暴露出这些问题：

| # | 问题 | 现象 |
|---|---|---|
| 1 | 拉不下来 | 模块路径是 `tenon`，不是可下载的地址，只能 `replace tenon => <本地路径>`；没有版本标签 |
| 2 | 公开接口漏到内部包 | 这个最小程序 import 了 7 个框架包：`tenon`、`runtime/agentdef`、`runtime/assemble`、`runtime/toolhub`、`runtime/store/memstore`、`tenontest`。`types.go` 的别名只覆盖了 Registrar、Scope、Store 等几个 |
| 3 | 不能多轮对话 | `Ask` 每次新开会话；`Reply` 的注释说"后续可用 Session 继续"，但没有这个方法。要接着聊只能绕到 `Host()` 或协议层 |
| 4 | 没有流式 | `Ask` 阻塞到回答结束；要逐字输出只能用会话钩子或协议连接 |
| 5 | 起步门槛 | `Ask` 必须给定义名，没有缺省智能体；定义只能写 TOML 字符串或放目录；配真实模型要 `WithConfig("models", map{"base_url": …})`，密钥固定读环境变量 `TENON_API_KEY` |
| 6 | 选项平铺 | 约 22 个 `With…` 在同一层，嵌入用的和应用宿主用的混在一起；8 个访问器（`Host()`、`Assembly()`…）直接暴露内部对象，等于把它们也算成了公开面 |
| 7 | 扩展点靠字符串 | `cx.Register("session_tools", 名, any)`，类型写错要到装配时才报；取服务那边已经有类型化的 `ext.Key[T]`，登记这边没有 |
| 8 | 没有上手文档 | `docs/框架设计/` 是设计文档；仓库 README 讲目录 |

## 三、改法

### 3.1 外观：常规应用只用 `tenon`

新增的公开类型与函数都在模块根，内部包不变；已有接口全部保留。

**工具**

```go
// 参数用结构体描述，JSON Schema 按字段与 tag 生成（json、desc、enum、可选用 omitempty）。
type OrderArgs struct {
    ID string `json:"id" desc:"订单号"`
}

lookup := tenon.Func("order_query", "按订单号查订单状态",
    func(ctx context.Context, a OrderArgs) (any, error) {
        return map[string]string{"id": a.ID, "status": "已发货"}, nil
    })

app, _ := tenon.New(tenon.Default(), tenon.WithTools(lookup, other.Dangerous()))
```

- `tenon.Tool`：不透明类型，包住 `toolhub.Tool`。`Func[T]` 生成 schema、解码参数、把返回值编成 JSON 文本；`Raw(name, desc, schema, fn)` 给要自己写 schema 的。
- 链式修饰：`.Dangerous()` / `.Writes()`（风险级别，决定要不要确认）、`.Timeout(d)`、`.Serial()`。
- `tenon.WithTools(...)`：SDK 内部生成一个组件，登记到 `session_tools` 点位。需要按会话决定显不显露的，仍然写组件。
- 工具里要会话信息的，用 `tenon.CallInfo(ctx)` 取会话 id、租户、用户（从 `toolhub.Cx` 转出来）。

**智能体定义**

```go
app, _ := tenon.New(tenon.Default(),
    tenon.WithAgent(tenon.Agent{Name: "support", Prompt: "你是售后客服", Tools: []string{"order_query"}}))
```

- `tenon.Agent{Name, Description, Prompt, Model, Tools, Use}`：等同 `agent.toml`，SDK 转成内存定义；`Dir(root)` 照旧读目录。
- 缺省智能体：`Ask(ctx, "", …)` 时，只有一个定义就用它；一个都没有就用内置的 `default`（中性提示词、全部显露的工具）。两个以上不给名字报错，错误信息列出可选名字。

**存储**

`tenon.MemoryStore()`、`tenon.SQLiteStore(path)`：转一层构造函数，不必 import `runtime/store/...`。

### 3.2 模型与凭证

```go
tenon.WithOpenAI("https://api.deepseek.com/v1", "deepseek-chat", tenon.Env("DEEPSEEK_API_KEY"))
tenon.WithAnthropic("claude-sonnet-4-5", tenon.Env("ANTHROPIC_API_KEY"))
tenon.WithModelCatalog("models.toml")          // 多个提供方、别名、降级计划
tenon.WithSecrets(func(ref string) (string, error) { … }) // 接自己的密钥库，填 slot:secrets
```

- `tenon.Env(name)` 生成密钥引用 `env:NAME`。不提供写明文密钥的选项：密钥只以引用出现（model-hub D4）。
- models 插件的配置加 `api_key`（引用，缺省仍是 `env:TENON_API_KEY`，现有 profile 不受影响）；`WithOpenAI` 等是在它上面的类型化写法，代替 `WithConfig("models", map…)`。

### 3.3 多轮与流式

```go
s, _ := app.NewSession(ctx, "support", tenon.Meta{"user": "u1"})
r, _ := s.Send(ctx, "我的订单 A1 到哪了？")       // 阻塞到回答结束
r, _ = s.Send(ctx, "那 A2 呢？")                  // 同一个会话接着聊

for ev := range s.Stream(ctx, "帮我退掉 A2") {   // 流式
    switch ev.Kind {
    case tenon.EventText:       fmt.Print(ev.Text)
    case tenon.EventToolCall:   fmt.Printf("\n[调用 %s]\n", ev.Tool)
    case tenon.EventConfirm:    ev.Approve(true)   // 危险工具要确认
    case tenon.EventDone:       …
    }
}

s, _ = app.Session(ctx, id)  // 按 id 找回已有会话
```

- `Session` 包住宿主的会话句柄：`Send`、`Stream`、`Cancel`、`History`、`ID`。`Ask` 保留，等于 `NewSession` + `Send`。
- 事件从协议层的同一份流式事件转过来（`message.delta`、`tool.started`、`interaction.opened`、`turn.ended`），不另造一套；SDK 用户和前端看到的是同一种东西。
- 确认：事件里带 `Approve(bool)`，内部走 `interaction.respond`。没人处理时按 `WithProtocolUI` / `WithUI` 的现有规则。

### 3.4 选项分组与逃生口

- 根包的选项分两组写进文档：**应用**（预设、存储、模型、智能体、工具、工作目录、界面、内核参数）与**应用宿主**（`WithProfile`、`WithService`、`WithWorkspace`、`WithSessionHooks`、`WithModeApprovals`、`WithProtocolOptions`、`WithReadyErrors`）。宿主组只在 godoc 和文档里归类，不挪包（挪包会让 `apphost` 和 SDK 互相引用）。
- 8 个访问器收成一个 `App.Internals() Internals`（结构体：Host、Bus、Assembly、Models、Definitions、DB、Blobs、Builder），文档写明"不在稳定性承诺里"。`apphost` 和 CLI 改用它。

### 3.5 类型化扩展点

```go
// runtime/ext：点位也有类型
var SessionTools = NewPoint[assemble.ToolsEntry]("session_tools")

// 组件里：
component.RegisterAt(cx, ext.SessionTools, "orders", assemble.ToolsEntry{…}) // impl 类型不对，编译不过
```

- 现有的 `Register(point, name, any)` 保留（外置插件与动态场景要用），内置插件逐个改成类型化写法。
- 根包导出常用点位的别名：`tenon.PointTools`、`tenon.PointSystemBlock`、`tenon.PointLayer`、`tenon.PointAfterTurn`，写组件的人不必 import `runtime/ext`。

### 3.6 文档与示例

- `docs/上手/`：
  1. 十分钟：一个带工具的智能体（3.1 + 3.2）；
  2. 多轮与流式（3.3）；
  3. 写组件（点位、服务、配置、守卫）；
  4. 测试（`tenontest` 脚本模型、断言工具调用）；
  5. 接前端（`App.API()` + `transport/web` + `@tenon/web`）。
- `examples/` 按上面每一篇一个可运行程序（`hello`、`tools`、`chat`、`component`、`web-server` 已有）。示例在模块里，`go vet ./...` 就会编译它们；脚本模型的示例在 CI 里真的跑一遍。
- 根包 godoc 写成目录页：从 `New` 到各组选项、`Session`、写组件，各给一个最短例子（`Example` 测试函数，`go test` 会执行）。

### 3.7 公开面清单与快照

- `docs/框架设计/07 - SDK 接口.md` 改成公开面清单：哪些包、哪些名字在承诺里（根包、`tenontest`、`transport/*`、`adapters`、`apphost`、`appapi/wire`），其余 `runtime/*`、`modules/*` 写明可能变。
- 快照测试：用 `go/types` 列出承诺包的全部导出名与签名，写进 `testdata/api.txt`；不一致时失败，提示"公开接口变了：更新快照并写决策日志"。不依赖要联网安装的工具。

### 3.8 模块路径与发布

- **拆仓库**（2026-11-08 定，见第五节第 4 条）：框架独立成仓库（准备公开），`framework/` 提到仓库根，用 `git filter-repo --subdirectory-filter framework` 保留历史；产品仓库（保持私有）放 `client/`、`saas/`、`archive/`。
- 模块路径从 `tenon` 改为可下载的地址（自有域名 `tenon.<域名>`，框架在仓库根，`go-import` 不需要子目录字段），所有 import 机械替换。产品仓库 `require` 锁定框架版本；本地开发用 `go.work` 指向相邻的框架目录，前端 `link:` 指向相邻目录的 `web/`；产品 CI 拉锁定版本的框架。
- 跟着搬：框架 `scripts/` 里构建客户端命令行的验收脚本（`accept_m*.sh`、`accept_p0.sh`、`accept_perm.sh`、`m7_round*.sh`）搬到产品仓库，或改成用框架示例测；`ci.sh` 里跑客户端的一段搬到产品 CI；`serve-web.sh` 搬到产品仓库；偏产品的计划文档（05 前后端跟进清单、06 界面重设计方案）搬到产品仓库；空的 `client/internal/` 删掉。
- 打 `v0.1.0` 标签（框架在仓库根，不带前缀）；v0 期间接口可以改，但每次改写进决策日志与快照。
- `@tenon/web` 是否发 npm：先不发，示例与文档用 `link:`；有外部用户再定。

## 四、分阶段

| 阶段 | 内容 | 是否破坏现有代码 | 规模 |
|---|---|---|---|
| A 外观 | 3.1、3.2、3.3、3.6 的上手 1–2 篇和对应示例 | 不破坏，只加 | 中：根包新增约 800 行 + 测试；models 插件加一个配置项 |
| B 收口 | 3.4、3.5、3.6 其余、3.7 | `App.Host()` 等访问器换成 `Internals()`，apphost / CLI 跟着改 | 中：内置插件逐个改类型化登记 |
| C 拆仓库与发布 | 3.8 | 全部 import 路径变化，产品改为按版本引用框架 | 面广：历史拆分、脚本替换、两边 CI 全跑 |

顺序 A → B → C（2026-11-08 定）：A、B 都要同时改框架和产品，在一个仓库里做最省事；拆仓库和改模块路径都会改动全部 import，放在 C 一起做，只改一次；框架现在没有外部用户，晚拆不耽误。

每个阶段的验收：阶段 A 结束时，第二节那个外部程序改写成只 import `tenon` 与 `tenontest`、接真实模型、两轮对话加一次流式，三十行左右；阶段 C 结束时，在空目录里按上手文档从 `go get` 开始跑通。

## 五、已定事项（2026-11-08 按建议定）

1. **模块路径。** 现状：仓库是 `github.com/Sevyn17/tenon`（个人账号下的单仓库，框架在 `framework/` 子目录）。模块路径一旦有人用就不能改（改了所有使用者都要改 import），所以要一次定对。
   - 建议：自有域名做导入路径，如 `tenon.<域名>`，静态页的 `go-import` 用第四个字段指到 `framework/` 子目录（Go 1.25 起支持；我们最低要求 1.26，不受限制）。好处：路径短、和账号与仓库布局解耦（以后转到组织、拆仓库都不影响使用者），单仓库照旧。代价：要一个域名，加一个静态页（GitHub Pages 即可）。
   - 没有域名时：先把仓库转到一个组织账号下，路径用 `github.com/<组织>/tenon/framework`；不建议用个人账号的路径对外。
   - 两种方案的版本标签都要带子目录前缀：`framework/v0.1.0`。仓库要公开，否则别人 `go get` 要配 GOPRIVATE 和凭据。
   - **已被第 4 条修订**：框架独立成仓库后在仓库根，`go-import` 不需要子目录字段，标签不带前缀（`v0.1.0`）；自有域名的做法不变。
2. **缺省智能体。** 现状：定义里不写 `tools` 就是"不收窄"，拿到全部工具；需要确认的调用会等确认，缺省截止 24 小时（`confirm_deadline`）。所以"没定义就用一个拿全部工具的缺省智能体"不安全：`Default()` 预设里有 shell 和写文件，没有界面回答时 `Ask` 会一直等。
   - 建议的规则：
     - 给了名字：按名字找，找不到报错（同现在）；
     - 没给名字、只有一个定义：用它；
     - 没给名字、一个定义都没有：用内置 `default`——中性提示词，只带应用自己用 `WithTools` 加的工具和只读的内置工具；要写文件、跑命令，写一个定义把工具列出来；
     - 没给名字、有多个定义：报错并列出可选名字；`WithDefaultAgent(名字)` 可以指定。
   - 顺带修：没有界面也没有协议客户端时，需要确认的调用立即拒绝并说明原因，不等 24 小时（阶段 A 里先核实现状再改）。
3. **Go 版本。** 现状：Go 1.27 已于 2026-08-19 发布，官方只维护最新两个版本（1.27、1.26），1.26 是还在维护的最老版本；直接依赖（modernc sqlite、goja、x/net、Wails v3）的 go.mod 都已经要求 1.26.0，想放低也放不下去。
   - 建议：保持 1.26，不急着升 1.27（升了会把还在 1.26 的使用者挡在外面，泛型方法、JSON v2 这些新东西我们暂时用不上）。定成规则：最低版本跟"官方还在维护的最老版本"走，每次 Go 新版发布后半年内评估一次。
4. **框架独立仓库（2026-11-08 定，修订第 1 条的"单仓库不拆"）。** 现状：`github.com/Sevyn17/tenon` 是私有仓库，里面有框架、客户端、SaaS、归档。第 1 条要求仓库公开，那样产品代码会一起公开；框架想公开、产品想保密，只能分成两个仓库。另外，产品只能按版本引用框架，等于每天都在用对外接口，接口泄漏会最先暴露。代价：同时改两边要两个提交（最近 60 个提交里 11 个同时动了两边，主要是边界迁移那一段），用 `go.work` 和 `link:` 把本地开发体验保持住。时机：放在阶段 C，和改模块路径一起做。做法见 3.8。
