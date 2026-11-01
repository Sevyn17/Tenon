# ybase-kernel

本地智能体内核与 CLI。内核只提供机制，工具、权限、沙箱、提示词等都是插件，由插件管理器按 profile 装配。

## 运行

```powershell
# 推荐：加载 kernel/.env 中的 DEEPSEEK_API_KEY，编译到 %LOCALAPPDATA%\ybase 后启动
./scripts/ybase.ps1 -dir D:\Code
```

常用参数：`-dir` 工作目录、`-session` 会话名（同名续聊）、`-model`、`-mode`（read-only / ask / auto / full / custom，缺省 auto）、`-max-steps`、`-trust-project`。会话内命令：`/mode` `/max-steps` `/plugins` `/rewind` `/compact` `/fork` `/stats` `/exit`。子命令：`ybase explain`（看装配结果）、`ybase trust`、`ybase sandbox-reset`、`ybase export`。

## 文档

从 [00 - 总体架构](<docs/00 - 总体架构.md>) 读起，它说明三层划分、内核包、插件全表、外置插件、前端与多端接入、目录规划和路线。其余文档：

| 编号 | 内容 |
|---|---|
| [01 - 路线图](<docs/01 - 路线图.md>) | 阶段目标与退出判据 |
| [02 - 任务总清单](<docs/02 - 任务总清单.md>) | 里程碑任务与决策记录 |
| [03 - 约束清单](<docs/03 - 约束清单.md>) | 内核与插件的分界原则、所有限制和可调参数 |
| [10 - 框架模块](<docs/10 - 框架模块/00 - 索引.md>) | 各内核模块的定稿方案 |
| [20 - 插件](<docs/20 - 插件/>) | 每个插件的调研与结论 |
| [30 - 计划与报告](<docs/30 - 计划与报告/>) | 开发计划、验收报告、试用指南、改造方案 |

## 测试

```powershell
go vet ./...
go test -race ./...
```
