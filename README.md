# Tenon

Go 智能体框架（Go 模块 `tenon`）。运行时（`runtime/`）只提供机制：会话、信封、检查点、崩溃恢复、守卫；工具、权限、沙箱、提示词等能力由标准模块（`modules/`）的组件提供，应用经 SDK（模块根 `package tenon`）或 profile 装配。CLI、桌面端、网页服务在 `client/`。

## 运行

```powershell
# 推荐：加载 framework/.env 中的 DEEPSEEK_API_KEY，编译到 %LOCALAPPDATA%\tenon 后启动
./scripts/tenon.ps1 -dir D:\Code
```

常用参数：`-dir` 工作目录、`-session` 会话名（同名续聊）、`-model`、`-mode`（read-only / ask / auto / full / custom，缺省 auto）、`-max-steps`、`-trust-project`。会话内命令：`/mode` `/max-steps` `/plugins` `/rewind` `/compact` `/fork` `/stats` `/exit`。子命令：`tenon explain`（看装配结果）、`tenon trust`、`tenon sandbox-reset`、`tenon export`。

## 文档

从 [00 - 索引](<docs/00 - 索引.md>) 读起，它说明各目录的用途和以哪篇为准：

| 目录 / 文件 | 内容 |
|---|---|
| [框架设计](<docs/框架设计/00 - 索引.md>) | 框架目标态设计，总方案 |
| [计划](<docs/计划/01 - 开发计划.md>) | 开发计划、进度与决策日志 |
| [03 - 约束清单](<docs/03 - 约束清单.md>) | 内核与插件的分界原则、所有限制和可调参数 |
| [调研报告](<docs/调研报告/>) | 外部文章与产品调研 |
| [90 - 归档](<docs/90 - 归档/>) | 旧总体架构、路线图、任务清单、模块与插件文档，只读 |

## 测试

```powershell
go vet ./...
go test -race ./...
```
