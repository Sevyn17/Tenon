> **已归档（2026-11-01）**：本篇只读，不再修改。当前设计以 [框架设计](<../../框架设计/00 - 索引.md>) 为准，计划以 [计划](<../../计划/01 - 开发计划.md>) 为准。

# openai-responses 适配器（M6.7）

## 结论

- 协议名 `openai-responses`，实现 `kernel/modelhub/responses.go`（`OpenAIResponses`），与 openai-chat、anthropic-messages 一起在 `models` 插件里登记。
- 无状态用法：`store=false`，每次发送完整 `input`，不用 `previous_response_id`。原因：会话日志是唯一事实，服务端状态在崩溃恢复、改写（压缩 / 回退 / fork）后无法对齐；完整前缀也让提示缓存按前缀命中。
- 请求映射：系统块 → `instructions`；user → `message`/`input_text`；assistant 文本 → `message`/`output_text`；工具调用 → `function_call{call_id,name,arguments}`；工具结果 → `function_call_output{call_id,output}`；工具 → 扁平 `{type:function,name,description,parameters}`；`max_tokens` → `max_output_tokens`；`prompt_cache_key` 按 compat 开关；`Params.Extra` 原样合并。
- 推理：请求带 `include: ["reasoning.encrypted_content"]`；响应的 reasoning 项存为 `ReasoningBlock{Text: 摘要, Opaque: {protocol:"openai-responses", encrypted_content}}`，回放时只带回同协议且有加密内容的块（03 #9，其他协议的推理丢弃）。
- 流解析：`response.output_text.delta` → TextDelta；`response.reasoning_summary_text.delta` → ReasoningDelta；`response.output_item.done` 收集完整项（function_call / reasoning / message）；`response.completed` 取 usage（`input_tokens`、`output_tokens`、`input_tokens_details.cached_tokens` → CacheRead）与 stop；`response.failed` / `error` 归一为失败 code（含上下文超长）；`response.incomplete` 以 `incomplete_details.reason` 作 stop。未收到 completed 即断流 → TRANSPORT。HTTP 错误复用 `httpFailure`。
- 目录：内置提供方 `openai-responses`（同 OpenAI 密钥），模型引用 `openai-responses/<模型>`；`openai` 提供方仍走 chat。

## 不做

- 内置工具（web_search、file_search、computer_use）、`previous_response_id`、后台模式、图片输入。
- `apply_patch` 自由格式工具：等真正接 GPT-5 / Codex 模型族时以插件提供（5.1 决定）。

## 参考

- OpenAI Responses API 流事件；Codex codex-rs 的 responses 客户端（同样 store=false + encrypted_content）。
