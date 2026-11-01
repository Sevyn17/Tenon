# AGENTS.md（M5.6 调研与结论）

## 参考
- Codex `core/src/agents_md.rs`：从 cwd 向上找项目根（标记默认 `.git`），从根到 cwd 每层取 `AGENTS.override.md` 优先、否则 `AGENTS.md`（可配 fallback 文件名）；总预算 `project_doc_max_bytes`（32KiB），超出截断；多文件用分隔符拼接；用户全局 `~/.codex/AGENTS.md` 在前。
- DSH `dsh-agent-instructions`：用户全局链在前，项目链从根到工作目录由宽到具体；候选 `AGENTS.md` / `CLAUDE.md`，本地叠加 `AGENTS.local.md` / `CLAUDE.local.md`；同组内容相同只渲染一次；`maxBytes` 必填（base 65536）；成功的 read / write / edit 到达更深目录后，下一次请求追加新适用的指令文件（仅追加，不破坏前缀）；根标记探测的 I/O 错误直接失败，不换祖先。
- pi-mono `resource-loader.ts`：同样从根到 cwd 收集 `AGENTS.md` / `CLAUDE.md`。

## 结论
1. **基线**：会话创建时装载一次，作为系统块 `Source: "agents_md"`（放在 agent 提示词之后）。顺序：用户全局 `~/.ybase/AGENTS.md`（或 `$YBASE_HOME/AGENTS.md`）→ 项目根到工作目录每层。每层候选 `AGENTS.md`，没有时用 `CLAUDE.md`；再叠加 `AGENTS.local.md`。内容（去首尾空白）与已装载的相同则跳过。每个文件带 `# 来自 <相对路径>` 标题。
2. **项目根**：从工作目录向上找 `.git`；找不到只用工作目录本身。探测遇到“不存在”以外的错误直接报错（DSH 教训）。
3. **预算**：渲染后总字节上限默认 64KiB（与 DSH base 一致），单文件上限 32KiB；超出部分截断并注明“已截断”。按顺序先到先得，越具体越靠后，因此预算不足时截掉的是最深的文件——这与 Codex 相同，记入决策日志。
4. **嵌套发现**：工具 Transform 层。read_file / edit / write_file / grep / glob 成功后，取参数 `path` 所在目录，若它在工作目录之下且路径上有尚未装载的 `AGENTS.md`，把内容追加到这次工具结果末尾（仅追加，不改系统提示词，前缀缓存不受影响）。已装载集合在内存；进程重启后可能再追加一次同一文件，代价是少量重复 token，换来不另存状态，记入决策日志。
5. 不做：文件变更后的替换 / 移除通知（DSH 有），推迟。
