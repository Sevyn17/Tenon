# 07 SDK 接口

> 状态：草案（2026-11-01）。列出应用开发者直接用到的公开接口，以及它们怎样演进。组件开发者用到的接口见 [04 扩展模型](<04 - 扩展模型.md>)。

## 7.1 最小例子

```go
func main() {
    app, err := ybase.New(
        ybase.Default(),                          // 标准组件预设（10）
        ybase.WithStore(sqlite.Open("data.db")),
        ybase.WithModels(openai.FromEnv()),
        ybase.WithAgents(ybase.Dir("./agents")),  // 定义来源（03 3.5）
        ybase.With(&shop.Orders{}),               // 应用自己的组件
    )
    if err != nil { log.Fatal(err) }
    defer app.Close(context.Background())

    ctx := ybase.WithTenant(context.Background(), "t1")
    ctx = ybase.WithUser(ctx, "u42")
    reply, err := app.Ask(ctx, "support", "我的订单到哪了？", nil)
}
```

## 7.2 装配选项

| 选项 | 作用 |
|---|---|
| `Default()` | 装上标准组件组合 |
| `With(c ...Component)` | 加组件 |
| `Without(names ...string)` | 从预设里去掉组件 |
| `WithStore(Store)` | 存储实现（05） |
| `WithModels(...ModelProvider)` | 模型供应商；档位 `fast` / `strong` 映射到具体模型 |
| `WithAgents(...SpecSource)` | 定义来源，可多个，按命名空间（平台 / 租户 / 用户）区分，不做字段合并（03 3.5，D7） |
| `WithConfig(path)` / `WithConfigSource(ConfigSource)` | 配置（06） |
| `Override[T](impl)` | 替换某个服务（04 4.4） |
| `WithOrder(point, names...)` | 显式指定中间件顺序（04 4.3） |

`New` 只装配和校验，不启动后台工作；必须显式调用 `app.Start(ctx)` 才启动，未启动时调用运行接口返回错误（D6）。所有校验错误在 `New` 里一次报完，不是遇到第一个就返回。

## 7.3 运行接口

```go
type App interface {
    // 和智能体新建会话并发第一条，等到本轮结束；是 NewSession + Send 的简写
    Ask(ctx context.Context, agent, text string, meta Meta, opts ...RunOption) (*Reply, error)

    // 发一封信封，不等结果；to 可以是 session: / agent: / human: 等（02 2.3）
    // 只传目标和正文：from、origin、hop、caps、budget_ref、trace、corr 由内核写入（D2、D3）
    // session:<id> 只查不建：找不到返回 ErrSessionNotFound，已关闭返回 ErrSessionClosed（13 13.3）
    // agent:<名字> 新建会话；WithKey(key) 时按 (租户, 定义, 创建者, key) 原子地查或建（会话键，B19 修订；模型工具不开放）
    // 返回投递到的会话 id 和 trace id
    Send(ctx context.Context, to Address, body Body, opts ...SendOption) (SendResult, error)

    // 会话
    Session(ctx context.Context, id SessionID) (Session, error)
    NewSession(ctx context.Context, agent string, meta Meta) (Session, error)
    Sessions(ctx context.Context, q SessionQuery, cur Cursor) ([]SessionInfo, Cursor, error) // 按定义、状态、meta 键查，分页

    // 订阅事件（04 4.6），从 cursor 之后开始；cursor 可持久化，断线后续读
    Events(ctx context.Context, after Cursor, filter EventFilter) (<-chan Event, error)

    // 回答人工确认 / 提问；底座核对 ctx 里的用户就是被问的人、corr 未过期未用过（D3）
    Respond(ctx context.Context, corr string, answer Answer) error

    // 诊断
    Explain(ctx context.Context, q ExplainQuery) (*Explanation, error)

    Start(ctx context.Context) error
    Close(ctx context.Context) error
}

type Session interface {
    ID() SessionID
    Send(ctx context.Context, text string, opts ...RunOption) (*Run, error)   // 返回的 Run 可流式读
    History(ctx context.Context, from, limit int) ([]Entry, error)
    Fork(ctx context.Context, at int) (Session, error)
    Rewind(ctx context.Context, at int) error
    Cancel(ctx context.Context) error                  // 中断当前一轮（02 2.2 已中断）
    Resume(ctx context.Context) error                  // 预算挂起后显式恢复
    Migrate(ctx context.Context, version string) error // 迁到定义的新版本（03 3.4）
    SetMeta(ctx context.Context, m Meta) error         // 应用改 meta，模型改不了（05 5.4）
    Close(ctx context.Context) error                   // 归档
}

// 一轮的结果；Run 可流式读增量，Wait 等到本轮结束
type Run interface {
    Stream() <-chan Delta
    Wait(ctx context.Context) (*Reply, error)
}
```

错误类型：`ErrNotStarted`（未 `Start`）、`ErrDenied`（守卫拒绝，带原因）、`ErrBudget`（预算用尽）、`ErrNotFound`、`ErrSessionNotFound`（`session:` 地址找不到会话）、`ErrSessionClosed`（会话已关闭，发送方应新建会话）、`ErrConflict`（租约或条件写冲突）、`ErrInvalid`（定义或参数校验失败），都能用 `errors.Is` 判断。

租户和用户是一等字段，不放 `meta`：应用用 `ybase.WithTenant(ctx, t)`、`ybase.WithUser(ctx, u)` 写进调用上下文，底座把它们记为会话的租户和信封的发送者 `from`（02 2.4、08 8.3）。取不到租户的调用直接报错（08 8.8）；单机应用用 `ybase.WithTenant(ctx, "local")` 或装配时设缺省租户。

多人会话：每个用户用自己的 ctx 调 `Session.Send`，发送者各不相同；谁能往这个会话发，由成员组件的守卫规则决定（08 8.2）。

`meta` 由应用写入，放其余业务键（如店铺、渠道），模型改不了（05 5.4）。

## 7.4 对外协议

同一套接口通过 `ybase/server` 暴露成 HTTP + WebSocket（或 SSE），供非 Go 应用和界面使用。路径和 Go 方法一一对应，OpenAPI 文档由接口定义生成。协议和 Go SDK 用同一个版本号。

鉴权由应用提供：`server.WithAuthenticator(func(*http.Request) (tenant, user string, err error))`，底座用返回值调用 `WithTenant` / `WithUser`。没有配置 Authenticator 时服务只监听本机回环地址。

## 7.5 事件名

| 事件 | 时机 |
|---|---|
| `definition.loaded` / `definition.rejected` | 定义加载成功 / 校验失败 |
| `session.created` / `session.active` / `session.idle` / `session.closed` | 会话状态变化（02 2.2） |
| `session.suspended` / `session.resumed` / `session.forked` / `session.rewound` / `session.migrated` | 预算挂起、恢复、Fork、Rewind、迁版本 |
| `turn.started` / `turn.done` / `turn.failed` / `turn.interrupted` | 一轮运行（02 2.4） |
| `lease.acquired` / `lease.lost` | 租约变化（02 2.2） |
| `plugin.crashed` | 外部插件崩溃重启（04 4.8） |
| `model.called` | 一次模型请求结束，带用量 |
| `tool.called` | 一次工具调用结束 |
| `mail.delivered` / `mail.rejected` | 信封投递 |
| `guard.denied` | 守卫拒绝（08） |
| `human.asked` / `human.answered` | 问人 |
| `budget.exceeded` | 预算用尽 |

组件可以加自己的事件，名字前缀必须是组件名：`group.posted`、`board.claimed`。

## 7.6 版本与弃用

- 包路径 `ybase/v1`，大版本才允许不兼容改动，大版本换包路径。
- 小版本内：可以加方法、加选项、加事件、加字段；不删、不改含义。
- 弃用：标 `// Deprecated:` 并在运行时第一次调用时打一条警告日志，**至少保留两个小版本**再在下一个大版本删除。弃用说明必须写替代方式。
- 定义包格式有自己的版本 `format = "ybase/v1"`（03 3.4），和 SDK 版本分开：新 SDK 读旧格式必须兼容，读到不认识的新格式直接拒绝。
- 事件结构只加字段不改字段；订阅者要容忍不认识的字段。
- `x.*` 和 `meta` 的内容底座永远原样保存，任何版本都不会去解释它们。

## 7.7 现状差距

| 现状 | 目标 | 处理 |
|---|---|---|
| 应用通过 `runtime.Builder` 拼装，接口偏内部 | `ybase.New` + 选项 | T1 |
| 没有对外承诺的接口边界 | 本篇列出的接口加版本规则 | T1 定稿，T2 起执行 |
| 界面已经走应用接口层 `core/appapi`（protocol 1，21 条命令，`App.Call` 分发；api-server 走 WebSocket JSON 帧，见 [10 - 框架模块/12](<../90 - 归档/10 - 框架模块/12 - 应用接口层.md>)、[14](<../90 - 归档/10 - 框架模块/14 - 网页服务.md>)）。但命令按会话 id + 文本设计（`session.send`），没有信封、地址、租户和用户 | appapi 是 7.4 协议的前身：T1 把 `appapi.App` 改成包在 `ybase.App` 外面的薄适配；T3 有了信封和地址后，命令改成 `Send(to, body)`、事件改用 7.5 的名字，protocol 升到 2 | T1 适配，T3 换协议 |
| api-server 只有随机令牌 + Host/Origin 检查，只认本机 | `server.WithAuthenticator` 返回租户和用户 | T3（随 `WithTenant` / `WithUser` 一起做） |
| appapi 错误码 `busy`、`not_idle`、`conflict`、`not_found` 等 | 7.3 的错误类型；appapi 码按类型映射 | T1 |
