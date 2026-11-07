// ?gallery=chat：会话组件的陈列（固定数据，不连后端）。用来审视对话区的视觉与动态效果：
// 已结束的回合（思考、工具组、正文、尾巴）、进行中的回合（流式正文、运行中的工具、等待确认的工具）、接管输入框的确认卡。

import { useEffect, useMemo, useState } from "react";
import { Copy, FileText, GitBranch, ImagePlus, Minimize2, RotateCcw, Settings2, ThumbsUp } from "lucide-react";
import {
  Composer,
  InteractionCard,
  MessageList,
  Segmented,
  TurnTail,
  applyTheme,
  buildTurns,
  type ComposerItem,
  type ThemePref,
  type UIAction,
  type UIEntry,
  type UIInteraction,
  type UIStream,
  type UITool,
} from "./ui";
import s from "./gallery.module.css";

const now = Date.now();

const entries: UIEntry[] = [
  { id: "u1", idx: 0, turn: 1, at: now - 90_000, item: { kind: "user", content: [{ kind: "text", text: "帮我看看 src/app.ts 里为什么启动慢，顺便把结论写进 NOTES.md。" }] } },
  {
    id: "a1",
    idx: 1,
    turn: 1,
    at: now - 88_000,
    item: {
      kind: "assistant",
      reasoning: [{ text: "先读 app.ts 看启动路径，再查有没有同步 I/O。启动时读了三个配置文件，都是同步的……" }],
      tool_calls: [
        { id: "c1", name: "read_file", args: { path: "src/app.ts" } },
        { id: "c2", name: "grep", args: { pattern: "readFileSync", path: "src" } },
      ],
    },
  },
  { id: "r1", idx: 2, turn: 1, at: now - 87_000, item: { kind: "tool_result", call_id: "c1", status: "ok", output: [{ kind: "text", text: "export async function start() {\n  const cfg = loadAll();\n  ...\n}" }] } },
  { id: "r2", idx: 3, turn: 1, at: now - 86_000, item: { kind: "tool_result", call_id: "c2", status: "ok", output: [{ kind: "text", text: "src/config.ts:12: readFileSync(a)\nsrc/config.ts:18: readFileSync(b)\nsrc/config.ts:25: readFileSync(c)" }] } },
  {
    id: "a2",
    idx: 4,
    turn: 1,
    at: now - 80_000,
    item: {
      kind: "assistant",
      model: "deepseek-chat",
      usage: { input: 12480, output: 412, cache_read: 10240, cache_write: 0 },
      content: [
        {
          kind: "text",
          text:
            "启动慢的原因是 `loadAll()` 在启动路径上**同步**读了三个配置文件：\n\n" +
            "| 文件 | 大小 | 耗时 |\n|---|---|---|\n| base.json | 120 KB | 38 ms |\n| user.json | 4 KB | 2 ms |\n| plugins.json | 2.1 MB | 410 ms |\n\n" +
            "建议改成并行异步读取：\n\n```ts\nconst [a, b, c] = await Promise.all(files.map((f) => readFile(f, \"utf8\")));\n```\n\n" +
            "> plugins.json 是主要瓶颈，可以只在用到插件时再加载。",
        },
      ],
    },
  },
  { id: "u2", idx: 5, turn: 2, at: now - 20_000, item: { kind: "user", content: [{ kind: "text", text: "好，按这个改，然后把结论写进 NOTES.md。" }] } },
  {
    id: "a3",
    idx: 6,
    turn: 2,
    at: now - 15_000,
    item: {
      kind: "assistant",
      tool_calls: [
        { id: "c3", name: "edit_file", args: { path: "src/config.ts" } },
        { id: "c4", name: "write_file", args: { path: "NOTES.md", content: "## 启动优化\n…" } },
      ],
    },
  },
  { id: "r3", idx: 7, turn: 2, at: now - 12_000, item: { kind: "tool_result", call_id: "c3", status: "ok", output: [{ kind: "text", text: "已修改 1 处" }] } },
];

const liveStreams: UIStream[] = [
  { stream_id: "st1", entry_id: "a4", part: "reasoning", text: "配置已经改完，接下来写 NOTES.md，等用户确认……", startedAt: now - 4000 },
];

const liveTools: UITool[] = [{ call_id: "c4", name: "write_file", args: '{"path":"NOTES.md","content":"## 启动优化\\n…"}', phase: "awaiting", startedAt: now - 6000 }];

const approval: UIInteraction = {
  id: "i1",
  session: "demo",
  kind: "approval",
  title: "write_file 需要确认",
  approval: {
    tool: "write_file",
    args: { path: "NOTES.md", content: "## 启动优化\n\n- 配置改为并行异步读取\n- plugins.json 延迟加载" },
    reason: "档位 每次询问：修改工作目录内的文件",
    session_rules: ["write_file(NOTES.md)"],
  },
  opened_at: now - 6000,
};

// 其余交互种类（choice / form / confirm）：陈列用，回答只记在本地。
const moreKinds: UIInteraction[] = [
  {
    id: "i2",
    session: "demo",
    kind: "choice",
    title: "周报用哪种格式？",
    choice: {
      multiple: true,
      max: 2,
      options: [
        { id: "md", label: "Markdown", description: "轻量，便于版本管理" },
        { id: "html", label: "网页", description: "浏览器直接打开" },
        { id: "pdf", label: "PDF", description: "适合打印，生成最慢" },
      ],
    },
    opened_at: now - 3000,
  },
  {
    id: "i3",
    session: "demo",
    kind: "form",
    title: "补充发布信息",
    blocking: false,
    form: { text: "智能体先继续写代码，填好后会用到。", submit: "保存" },
    schema: {
      type: "object",
      required: ["version"],
      properties: {
        version: { type: "string", title: "版本号" },
        notes: { type: "string", title: "发布说明", "x-widget": "textarea" },
        notify: { type: "boolean", title: "通知订阅者", default: true },
      },
    },
    opened_at: now - 2000,
  },
  {
    id: "i4",
    session: "demo",
    kind: "confirm",
    title: "清空构建缓存？",
    confirm: { text: "会删掉 .cache 下约 1.2 GB 文件，下次构建变慢。", danger: true, confirm: "清空" },
    opened_at: now - 1000,
  },
];

const items: ComposerItem[] = [
  { id: "file", group: "add", title: "文件或图片", description: "上传后作为附件发送", icon: <ImagePlus size={15} />, run: () => {} },
  { id: "ref", group: "add", title: "引用会话", description: "@ 另一个会话", icon: <FileText size={15} />, run: () => "@" },
  { id: "compact", group: "command", title: "压缩上下文", slash: "compact", description: "把早期对话总结成摘要", icon: <Minimize2 size={15} />, run: () => {} },
  { id: "fork", group: "command", title: "分叉会话", slash: "fork", icon: <GitBranch size={15} />, run: () => {} },
  { id: "mode", group: "command", title: "切换权限档位", slash: "mode", icon: <Settings2 size={15} />, run: () => {} },
  { id: "rewind", group: "command", title: "回退", slash: "rewind", icon: <RotateCcw size={15} />, disabled: true, reason: "回合进行中", run: () => {} },
];

export function GalleryChat() {
  const [theme, setTheme] = useState<ThemePref>("system");
  const [text, setText] = useState("");
  const [streamText, setStreamText] = useState("");
  const [resolved, setResolved] = useState<string | null>(null);
  const [answers, setAnswers] = useState<Record<string, { outcome: string; by?: string }>>({});
  useEffect(() => applyTheme(theme), [theme]);

  // 模拟正文流式：确认后逐字出现。
  useEffect(() => {
    if (!resolved) return;
    const full = "已写入 NOTES.md。启动时间从 **450 ms** 降到 **60 ms** 左右。";
    let i = 0;
    const t = setInterval(() => {
      i += 2;
      setStreamText(full.slice(0, i));
      if (i >= full.length) clearInterval(t);
    }, 60);
    return () => clearInterval(t);
  }, [resolved]);

  const streams: UIStream[] = resolved
    ? [{ stream_id: "st2", entry_id: "a5", part: "text", text: streamText, startedAt: now }]
    : liveStreams;
  const tools: UITool[] = resolved
    ? [{ ...liveTools[0], phase: "ok", output: "已写入 86 字节", endedAt: Date.now() }]
    : liveTools;

  const turns = useMemo(
    () => buildTurns({ entries, streams, tools, liveTurn: 2, awaiting: resolved ? undefined : new Set(["c4"]) }),
    [streams, tools, resolved],
  );

  const actions = (): UIAction[] => [
    { id: "copy", label: "复制", icon: <Copy size={14} />, placement: "hover", run: () => {} },
    { id: "retry", label: "重试", icon: <RotateCcw size={14} />, placement: "hover", run: () => {} },
    { id: "good", label: "有帮助", icon: <ThumbsUp size={14} />, placement: "hover", run: () => {} },
    { id: "fork", label: "从这里分叉", icon: <GitBranch size={14} />, placement: "menu", run: () => {} },
  ];

  return (
    <div className={s.chatPage}>
      <header className={s.chatHead}>
        <b>会话组件</b>
        <span className={s.grow} />
        <Segmented
          value={theme}
          onChange={(v) => setTheme(v as ThemePref)}
          options={[
            { value: "system", label: "跟随系统" },
            { value: "light", label: "浅色" },
            { value: "dark", label: "深色" },
          ]}
        />
      </header>
      <MessageList
        className={s.chatList}
        turns={turns}
        tick={streamText}
        actionsFor={(b) => (b.kind === "text" || b.kind === "user" ? actions() : undefined)}
        tail={(t) =>
          t.live ? null : (
            <TurnTail
              stats={{ durationMs: 10_400, steps: 2, tools: 2, input: 12480, output: 412, cacheRead: 10240, model: "deepseek-chat" }}
              actions={actions().slice(0, 2)}
              followups={["plugins.json 怎么延迟加载？", "还有别的启动瓶颈吗？"]}
              onFollowup={setText}
            />
          )
        }
      />
      <div className={s.chatKinds} data-gallery="interaction-kinds">
        {moreKinds.map((it) => (
          <InteractionCard key={it.id} it={it} done={answers[it.id]} onRespond={async (r) => setAnswers((a) => ({ ...a, [it.id]: { outcome: r.approve === false ? "denied" : r.approve ? "approved" : "answered", by: JSON.stringify(r.value ?? r.approve ?? r.text) } }))} />
        ))}
      </div>
      <div className={s.chatComposer}>
        <Composer
          value={text}
          onChange={setText}
          onSend={() => setText("")}
          onStop={() => {}}
          running
          items={items}
          queued={[{ id: "q1", text: "顺便跑一下测试", state: "sending" }]}
          takeover={
            resolved ? undefined : (
              <InteractionCard
                it={approval}
                keyboard
                onRespond={async (r) => setResolved(r.approve ? "approved" : "denied")}
                onDenyWithReason={async () => setResolved("denied")}
              />
            )
          }
        />
      </div>
    </div>
  );
}
