# Tenon

[![CI](https://github.com/Sevyn17/Tenon/actions/workflows/ci.yml/badge.svg)](https://github.com/Sevyn17/Tenon/actions/workflows/ci.yml)
[![CI (Gitea)](https://????/sevn/Tenon/actions/workflows/ci.yml/badge.svg?branch=main)](https://????/sevn/Tenon/actions)

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

要求 Go 1.26 及以上。v0 期间接口可能变，每次变动记在[决策日志](<docs/02 - 开发管理/02 - 决策日志.md>)里。

## 上手

1. [十分钟：带工具的智能体](<docs/00 - 上手指南/01 - 十分钟 - 带工具的智能体.md>)
2. [多轮与流式](<docs/00 - 上手指南/02 - 多轮与流式.md>)
3. [写组件](<docs/00 - 上手指南/03 - 写组件.md>)
4. [测试](<docs/00 - 上手指南/04 - 测试.md>)
5. [接前端](<docs/00 - 上手指南/05 - 接前端.md>)

示例在 [examples/](examples/)：`chat`、`hello`、`tools`、`component`、`web-server`。公开面（哪些包、哪些名字在稳定承诺里）见 [07 SDK 接口](<docs/01 - 设计文档/框架设计/07 - SDK 接口.md>)。

## 代码目录

| 目录 | 职责 |
|---|---|
| [runtime/](runtime/) | 内核机制：会话、宿主与回合循环、信封投递、注入、工具/模型枢纽、存储接口、装配 |
| [modules/](modules/) | 标准组件（可选）：fs、shell、web、mcp、permission、sandbox、提示词、技能、脱敏、压缩、subagent、workgroup、三家模型适配器、sqlite 存储 |
| [appapi/](appapi/) | 应用协议：命令、事件与线上类型（`wire`，前端 TS 类型由它生成） |
| 根包（`*.go`） | SDK：应用只 import 这一个包，[doc.go](doc.go) 是目录页 |
| [apphost/](apphost/) | 应用宿主装配：读 profile、装内置插件（命令行 / 桌面端 / 网页服务共用） |
| [adapters/](adapters/) | 插件能力 → 协议接口的适配器（模型列表、权限档位、建议追问、自动标题） |
| [transport/](transport/) | 协议载体：`web`（HTTP + WebSocket 服务）、`bind`（桌面壳进程内绑定） |
| [tenontest/](tenontest/) | 应用端到端测试套件：内存存储 + 脚本模型 + 断言 |
| [web/](web/) | 参考前端 `@tenon/web`：client（协议客户端）/ ui（设计系统）/ shell（工作台） |
| [examples/](examples/) | 上手文档的可运行对应物，CI 里编译并跑 |
| [docs/](docs/00%20-%20索引.md) | 按阶段分目录：设计文档、开发管理、上手指南、调研报告 |

依赖方向有测试强制（`runtime/assemble/deps_test.go`）：内核不 import 模块，模块之间不互相 import，装配层不被反向依赖。

## 文档

从 [00 - 索引](<docs/00 - 索引.md>) 读起：

| 目录 / 文件 | 内容 |
|---|---|
| [上手](<docs/00 - 上手指南/>) | 用框架写应用 |
| [框架设计](<docs/01 - 设计文档/框架设计/00 - 索引.md>) | 框架目标态设计 |
| [计划](<docs/02 - 开发管理/01 - 开发计划.md>) | 开发计划、进度与决策日志 |
| [03 - 约束清单](<docs/01 - 设计文档/03 - 约束清单.md>) | 内核与插件的分界原则、所有限制和可调参数 |
| [06 - 应用协议契约](<docs/01 - 设计文档/06 - 应用协议契约.md>) | 前端与服务端之间的协议 |
| [web/](web/) | 前端：协议客户端、hooks、组件、工作台 |

## 检查

```bash
bash scripts/ci.sh   # gofmt、vet、全部测试（含示例与公开面快照）、-race
```

不需要网络与 API 密钥。
