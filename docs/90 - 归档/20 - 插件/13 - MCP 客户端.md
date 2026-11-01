> **已归档（2026-11-01）**：本篇只读，不再修改。当前设计以 [框架设计](<../../框架设计/00 - 索引.md>) 为准，计划以 [计划](<../../计划/01 - 开发计划.md>) 为准。

# MCP 客户端（M6.3）

## 参考
- DSH `dsh-mcp-client`：一个服务一条配置（stdio / streamable-http）；工具名 `mcp__<server>__<tool>`，命名空间取本地配置名、不用远端 `serverInfo.name`；有损规范化时追加 12 位 SHA-256，保证不同身份不撞名；`tools/call` 只发原名；stdio 子进程环境去掉名字匹配 `KEY|PASSWORD|SECRET|TOKEN` 的变量，再合并配置里的 `env`；单次调用超时 60s；服务报错 → 调用失败（不伪装成功）；图片 / 音频 / 内嵌资源给明确提示；断线指数退避重连。
- Codex `rmcp-client`、`codex-mcp`：同样的命名空间与超时；启动时拉一次工具列表。
- tool-hub 定稿：T3 动态来源变化只影响新会话；T24 动态工具描述按不可信处理；D1 冻结可用集、超预算才延迟显露（MCP 标 Deferred）。

## 结论
1. 包 `kernel/plugins/mcp`，纯 Go 实现 JSON-RPC 2.0，不引第三方 SDK。传输：stdio（按行分隔 JSON）与 Streamable HTTP（POST，响应为 JSON 或 SSE；保存 `Mcp-Session-Id`，初始化后带 `MCP-Protocol-Version`）。协议版本请求 `2025-06-18`，接受服务端回的任意版本（记录在案）。
2. 命名：`mcp__<server>__<tool>`，server 名须匹配 `[A-Za-z0-9_-]{1,32}`；原名含非法字符或总长超 64 时替换为 `_`、截断并追加 `_` + 12 位哈希。调用只发原名。
3. 工具全部 `Deferred`（总量超预算时经 `tool_search` 显露）、不并行；描述截断到 2KiB 并标注来源服务。效果按服务的 `trust`（用户的判断，不信服务端自报）：`ask`（缺省）→ external 效果、`readonly` → read、`trusted` → write。注解只收紧：`destructiveHint: true`（非 readonly 服务）→ 每次询问、不提供“本会话允许”；`readOnlyHint` 不据此放行。
4. 工具列表在连接时拉取一次，之后不随 `list_changed` 变（会话内冻结，T3）；新会话用当时的列表。
5. 调用：默认超时 60s；回合取消时发 `notifications/cancelled`；`isError` → 工具错误结果；文本按块顺序拼接，`resource_link` 以名字 + URI 呈现，内嵌资源有文本则取文本，图片 / 音频给提示「未显示」。
6. stdio 环境变量去密钥（同 DSH 规则）后合并配置 `env`；stderr 只留尾部 8KiB 供诊断。子进程死掉后下次调用重连一次（启动时连接失败只告警，不阻止启动）。
7. 配置：项目 `.ybase/mcp.toml`（`[[server]] name/command/args/env/cwd/url/headers/timeout/trust/sandbox`，只在项目受信时读），CLI `-mcp <file>` 可另指。
8. stdio 子进程进沙箱：服务 `sandbox` 字段优先，否则取新会话缺省档位的 `sandbox.level`；无法放进沙箱时该服务不启动（告警）；沙箱插件未启用时不加沙箱并告警。子进程在启动时建立、进程级共享，会话切换档位不影响已启动的服务。
9. 已知缺口：不支持 prompts / resources / sampling / elicitation / OAuth；服务端发来的请求一律回 method not found。
