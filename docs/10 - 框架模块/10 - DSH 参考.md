# DeepSeek Harness 架构分析（参考）

> 状态：参考资料（lead 2026-10-28）
> 来源：`refer/deepseek-harness/packages/`（本机安装包 `app.asar` 解出，307 个包，编译后 JS + 中英 README）。本轮精读约 25 个包的 `README.zh.md`（概述 + 实现细节节），通读 `dsh-base/cordis.patch.yml`，读过 Cordis 源码 `context.ts` / `service.ts` / `fiber.ts`。**未读编译后 JS**；README 声称的行为未逐条用代码核实。

## 1. 总体形态

DSH = Cordis 插件框架 + 约 300 个小包。核心只有一个依赖注入容器，**会话、agent 循环、模型、工具、持久化、权限、压缩全部是插件**，由 profile 的 `cordis.patch.yml` 组合：

```text
profile（web / headless / sdk / acp）
  └─ bundle：dsh-base（共享核心）+ 各模式 bundle
       └─ 插件行：id / name / config / disabled，后写按 id 覆盖
            └─ 激活由服务是否就绪驱动，行顺序无加载语义（patch 头注释原文）
```

包按职责分层（按包名前缀归纳）：

| 层 | 代表包 | 职责 |
|---|---|---|
| 框架 | `cordis`、`cordis-plugin-loader/include/group/timer` | 容器、fiber 生命周期、配置树、热重载 |
| 会话 | `dsh-session`、`-persistence-jsonl`、`-checkpoint-policy`、`-format-*`（v0→v4 迁移）、`-projection`、`-query-sqlite` | 事件溯源日志、持久化后端、检查点时机、格式迁移、读模型 |
| 驱动 | `dsh-agent`、`dsh-agent-loop`、`dsh-agent-preset(-registry)` | Agent 句柄与注册表、默认循环、声明式 preset |
| 模型 | `dsh-llm`、`dsh-llm-retry`、`dsh-agent-default-model`、`dsh-system-prompt` | 单次流式调用、重试、默认模型、系统提示词组装 |
| 工具 | `dsh-tools`（注册表 + 流水线）、`dsh-tool-*`（约 30 个）、`dsh-tool-call-timeout-policy`、`dsh-repeat-tool-reminder`、`dsh-spill-policy` | 工具与工具策略分包 |
| 安全 | `dsh-sandbox(-local/-windows-acl/-policy)`、`dsh-fs-sandbox`、`dsh-bash/pwsh-sandbox`、`dsh-user-approval`、`dsh-permission-presets`、`dsh-authorization` | 沙箱机制、策略、审批、权限预设分开 |
| 上下文 | `dsh-compaction`（框架）+ `-basic` / `-image-offload` / `-tool-result-pruner`（后端）、`dsh-agent-instructions`、`dsh-skill(-filesystem)`、`dsh-time-context` | 压缩、AGENTS.md、技能、环境事实 |
| 协作 | `dsh-subagent(-spawn/-fork-in-process)`、`dsh-jobs-local`、`dsh-tool-goal`、`dsh-tool-ralph`、`dsh-experimental-agent-team` | 子智能体、后台任务、长程目标、团队 |
| 观测 | `dsh-session-telemetry(-otel)`、`dsh-session-log-deepseek`、`dsh-session-stats` | 遥测与日志上报 |
| 入口 | `dsh-acp`、`dsh-api-*`、`dsh-client-*`（Web GUI 约 60 个） | 协议、远程控制、前端 |

## 2. 值得学的机制

1. **机制与策略彻底分包。** 压缩框架 vs 压缩后端；沙箱机制 vs 沙箱策略 vs 审批；持久化后端 vs 检查点策略；`dsh-llm` 只尝试一次、重试在 `dsh-llm-retry`。每个包 README 都写"本包不做什么"。
2. **检查点策略是三道屏障**（`dsh-session-checkpoint-policy`）：模型请求构造流之前、顶层工具正文之前（在权限守卫之后）、每个 `agent/pre-step` 边界。失败即阻止：落盘不成功，适配器和工具正文都不运行。嵌套工具分派复用外层检查点。
3. **中断工具以"结果未知"闭合。** 检查点后、结果前崩溃的调用，恢复时追加 `TOOL_OUTCOME_UNKNOWN` 结果：只读或幂等的可以重试，可能有副作用的要先核对状态或问用户；不自动重试。
4. **模型可见输入必须能从日志重建**（`dsh-llm`）。Loop 构造的请求深度冻结，扩展和适配器不能改；`request/header` 只在工具或配置变化时写全量，其余步骤继承；`request/context`（提供方、模型、窗口）也只在变化时记。
5. **重试写日志。** `dsh-llm-retry` 在退避前把计划中的尝试写进会话日志，取消后历史仍一致；重试在步边界、同一回合内进行。
6. **溢出按 token 预算首尾保留**（`dsh-spill-policy`）：保留首尾、给出完整结果文件路径；图片进附件存储；无法保存可恢复内容时保留原结果。
7. **沙箱不降级。** 请求的模式无法强制执行时以 `SANDBOX_UNAVAILABLE` 失败，绝不以不受限方式运行；被拒后模型可请求严格更宽的模式，由人批准一次。
8. **审批默认拒绝。** `dsh-user-approval`：应答者缺失或失败即 `unavailable` → 拒绝；每项批准只对应一个请求；请求和结果写进会话审计日志。
9. **组合变化只影响新 Agent。** preset 与插件修改不改变已在运行的 Agent 的组合。
10. **指令有预算。** `dsh-agent-instructions`：字节预算内先省略宽泛文件、最后截断最具体的文件；靠文件系统工具的成功操作发现嵌套 AGENTS.md，不常驻监听。
11. **外部 harness 也是子智能体提供方**（`dsh-subagent`）：本地 spawn/fork 之外，ACP、DSH SDK、Codex、Claude Code 都可以作为一次性执行的子智能体，共享准入、取消、所有权。

## 3. 和我们的差异与取舍

| 维度 | DSH | 我们 | 取舍 |
|---|---|---|---|
| 可替换性 | 一切皆插件，会话与循环都可换 | 6 个模块不可替换，能力走点位 | **保持我们的**。DSH 用大量"本包不做什么"的纪律和测试换取灵活；我们把 S1–S7、Guard 最内侧锁在模块里，不靠纪律。DSH 的检查点策略可以被"刻意替换"（README 原文），我们不允许 |
| 存储模型 | 单一事件溯源日志 | 日志 + 头部 + 幂等记录 + 请求记录 | **保持我们的**（session-core 已定）。能力对齐：DSH 的 `request/header` = 我们的请求记录；`TOOL_OUTCOME_UNKNOWN` = 我们的 `started` → 状态未知 |
| 激活 | 服务就绪驱动、运行期热重载 | 装配期拓扑一次；插件级变更只对新会话生效 | **一致**。DSH 也只让新 Agent 看到新组合（第 2 条第 9 点），印证 plugin-manager L5 |
| 安全 | 沙箱、审批、预设分包，fail-closed | Guard 层强制最内侧 + mandatory 并集 | 语义相同；我们多一层"子 profile 不能移除强制项"。DSH 的"沙箱不降级 + 一次性升权"直接采纳进沙箱插件（52） |
| 粒度 | 一个关注点一个包 | 一个插件可注册多个点位 | 我们插件粒度更粗，P0 约 24 个插件；**采纳 DSH 的"机制 / 策略分开"作为插件拆分准则**（例：compaction 框架点位 + basic 后端；sandbox 机制 + policy） |

## 4. 对各模块文档的影响

- session-core / session-host：结论不变，补证据（见各篇 §3 补充）。
- plugin-manager：结论不变；新增插件拆分准则"机制与策略分开"。
- tool-hub：D6（独占屏障 + 有界滚动池）、D7（首尾保留）得到印证；D3 存工件阈值是否改按 token 估算，留给 P2 评测数据决定。
- model-hub（下一份）：带入三条输入——单次尝试与重试分开、重试尝试先写日志、请求冻结后不可改。
