# Tenon

Go 智能体框架。运行时（`runtime/`）只提供机制：会话、信封、检查点、崩溃恢复、守卫；工具、权限、沙箱、提示词等能力由标准模块（`modules/`）的组件提供，应用经 SDK（根包 `tenon`）装配。

```bash
go get github.com/Sevyn17/tenon
```

```go
import "github.com/Sevyn17/tenon"

app, err := tenon.New(tenon.Default(),
    tenon.WithStore(tenon.MemoryStore()),
    tenon.WithTools(lookup),
    tenon.WithOpenAI(baseURL, model, tenon.Env("TENON_API_KEY")))
```

要求 Go 1.26 及以上。v0 期间接口可能变，每次变动记在[决策日志](<docs/计划/02 - 决策日志.md>)里。

## 上手

1. [十分钟：带工具的智能体](<docs/上手/01 - 十分钟：带工具的智能体.md>)
2. [多轮与流式](<docs/上手/02 - 多轮与流式.md>)
3. [写组件](<docs/上手/03 - 写组件.md>)
4. [测试](<docs/上手/04 - 测试.md>)
5. [接前端](<docs/上手/05 - 接前端.md>)

示例在 [examples/](examples/)：`chat`、`hello`、`tools`、`component`、`web-server`。公开面（哪些包、哪些名字在稳定承诺里）见 [07 SDK 接口](<docs/框架设计/07 - SDK 接口.md>)。

## 文档

从 [00 - 索引](<docs/00 - 索引.md>) 读起：

| 目录 / 文件 | 内容 |
|---|---|
| [上手](<docs/上手/>) | 用框架写应用 |
| [框架设计](<docs/框架设计/00 - 索引.md>) | 框架目标态设计 |
| [计划](<docs/计划/01 - 开发计划.md>) | 开发计划、进度与决策日志 |
| [03 - 约束清单](<docs/03 - 约束清单.md>) | 内核与插件的分界原则、所有限制和可调参数 |
| [06 - 应用协议契约](<docs/06 - 应用协议契约.md>) | 前端与服务端之间的协议 |
| [web/](web/) | 前端：协议客户端、hooks、组件、工作台 |

## 检查

```bash
bash scripts/ci.sh   # gofmt、vet、全部测试（含示例与公开面快照）、-race
```

不需要网络与 API 密钥。
