// 消息流：把条目、正在流入的段、进行中的工具合成"回合"，按真实顺序渲染。
// 一个回合：用户气泡 → 过程（思考、工具步骤）与正文按出现顺序交错 → 尾巴。相邻工具调用合并成一组。
//
// 滚动：新内容到达时贴底；用户向上滚后不再跟随，底部出现"↓ 新内容"；滚到顶部附近加载更早的条目。
// 长会话用 content-visibility 让屏幕外的回合跳过布局与绘制（每个回合是不可变块，比虚拟列表更适合流式追加）。

import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ArrowDown } from "lucide-react";
import { cx } from "../cx";
import { Button } from "../controls";
import s from "./conversation.module.css";
import { AssistantMessage, Divider, Thinking, ToolSteps, UserMessage, partsText, type ToolView } from "./messages";
import type { UIAction, UIEntry, UIStream, UITool } from "./types";

/** 回合里的一块（按顺序）。 */
export type Block =
  | { kind: "user"; entry: UIEntry }
  | { kind: "thinking"; key: string; text: string; live: boolean; startedAt?: number }
  | { kind: "text"; key: string; text: string; streaming: boolean; interrupted?: boolean; entry?: UIEntry }
  | { kind: "tools"; key: string; tools: UITool[] }
  | { kind: "compaction"; entry: UIEntry }
  | { kind: "context"; entry: UIEntry };

export interface Turn {
  turn: number;
  blocks: Block[];
  entries: UIEntry[];
  live: boolean; // 进行中
}

/**
 * 条目 + 流 + 进行中的工具 → 回合。tools 是进行中（还没有结果条目）的调用；已有结果的调用从条目合成。
 * awaiting 是正在等确认的调用 id（步骤行显示"等待你的确认"）。
 */
export function buildTurns(p: {
  entries: UIEntry[];
  streams: UIStream[];
  tools: UITool[];
  liveTurn?: number | null;
  awaiting?: Set<string>;
}): Turn[] {
  const results = new Map<string, UIEntry>();
  for (const e of p.entries) if (e.item.kind === "tool_result" && e.item.call_id) results.set(e.item.call_id, e);
  const liveTools = new Map(p.tools.map((t) => [t.call_id, t]));
  const turns: Turn[] = [];
  const cur = (turn: number): Turn => {
    let t = turns[turns.length - 1];
    if (!t || t.turn !== turn) {
      t = { turn, blocks: [], entries: [], live: false };
      turns.push(t);
    }
    return t;
  };
  const pushTools = (t: Turn, key: string, tools: UITool[]) => {
    if (!tools.length) return;
    const last = t.blocks[t.blocks.length - 1];
    if (last && last.kind === "tools") last.tools.push(...tools);
    else t.blocks.push({ kind: "tools", key, tools });
  };
  const usedLive = new Set<string>();
  for (const e of p.entries) {
    const t = cur(e.turn);
    t.entries.push(e);
    const it = e.item;
    switch (it.kind) {
      case "user":
        t.blocks.push({ kind: "user", entry: e });
        break;
      case "assistant": {
        const reasoning = (it.reasoning ?? []).map((r) => r.text).join("\n");
        if (reasoning) t.blocks.push({ kind: "thinking", key: e.id + ":r", text: reasoning, live: false });
        const text = partsText(it.content);
        if (text) t.blocks.push({ kind: "text", key: e.id, text, streaming: false, interrupted: it.interrupted, entry: e });
        const tools: UITool[] = (it.tool_calls ?? []).map((c) => {
          const r = results.get(c.id);
          const lv = liveTools.get(c.id);
          if (lv) usedLive.add(c.id);
          const args = typeof c.args === "string" ? c.args : JSON.stringify(c.args ?? {});
          if (r) {
            const st = r.item.status ?? "ok";
            return {
              call_id: c.id,
              name: c.name,
              args,
              phase: st === "ok" ? "ok" : st === "cancelled" ? "cancelled" : "error",
              output: partsText(r.item.output),
              truncated: r.item.truncated,
              output_ref: r.item.output_ref,
            };
          }
          if (p.awaiting?.has(c.id)) return { call_id: c.id, name: c.name, args, phase: "awaiting", startedAt: lv?.startedAt };
          return lv ? { ...lv, name: lv.name || c.name, args: lv.args || args } : { call_id: c.id, name: c.name, args, phase: "running" };
        });
        pushTools(t, e.id + ":t", tools);
        break;
      }
      case "tool_result":
        break; // 并进对应调用的步骤行
      case "compaction":
        t.blocks.push({ kind: "compaction", entry: e });
        break;
      case "context":
        t.blocks.push({ kind: "context", entry: e });
        break;
    }
  }
  // 进行中：还没落库的流与工具接在最后一个回合（或新回合）后面。
  const live = p.liveTurn ?? null;
  if (live !== null) {
    const t = cur(live);
    t.live = true;
    for (const st of p.streams) {
      if (st.part === "reasoning")
        t.blocks.push({ kind: "thinking", key: st.stream_id, text: st.text, live: true, startedAt: st.startedAt });
      else t.blocks.push({ kind: "text", key: st.stream_id, text: st.text, streaming: true });
    }
    const rest = p.tools
      .filter((x) => !usedLive.has(x.call_id))
      .map((x) => (p.awaiting?.has(x.call_id) ? { ...x, phase: "awaiting" as const } : x));
    pushTools(t, "live:t", rest);
  }
  return turns;
}

export interface MessageListProps {
  turns: Turn[];
  /** 压缩点（之前的条目折叠成一条分隔）。 */
  base?: number;
  hasOlder?: boolean;
  loadingOlder?: boolean;
  onLoadOlder?: () => void;
  /** 每条消息的操作。 */
  actionsFor?: (b: Block, turn: Turn) => UIAction[] | undefined;
  /** 工具步骤的呈现（按工具名）。 */
  toolView?: (name: string) => ToolView | undefined;
  toolActions?: (t: UITool) => UIAction[] | undefined;
  /** 每个回合末尾（尾巴）。 */
  tail?: (turn: Turn, index: number, all: Turn[]) => ReactNode;
  /** 消息前后的插槽。 */
  slot?: (at: "user.after" | "assistant.after" | "turn.before" | "turn.after", turn: Turn, b?: Block) => ReactNode;
  /** 是否显示上下文注入条目（缺省隐藏）。 */
  showContext?: boolean;
  empty?: ReactNode;
  /** 列表底部（交互记录、接管卡之外的内容）。 */
  footer?: ReactNode;
  /** 变化计数：内容增长时用来判断是否要贴底（流式增量也算）。 */
  tick?: unknown;
  className?: string;
}

export function MessageList(p: MessageListProps) {
  const ref = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const [away, setAway] = useState(false);
  const prevHeight = useRef(0);
  const loadingRef = useRef(false);

  // 内容变化后贴底；向上加载更早条目后保持视口位置。
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (loadingRef.current && prevHeight.current) {
      el.scrollTop += el.scrollHeight - prevHeight.current;
      loadingRef.current = false;
    } else if (stick.current) {
      el.scrollTop = el.scrollHeight;
    }
    prevHeight.current = el.scrollHeight;
  }, [p.turns, p.tick]);

  useEffect(() => {
    if (!p.loadingOlder) loadingRef.current = loadingRef.current && false;
  }, [p.loadingOlder]);

  const onScroll = () => {
    const el = ref.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
    stick.current = atBottom;
    setAway(!atBottom);
    if (el.scrollTop < 80 && p.hasOlder && !p.loadingOlder && p.onLoadOlder) {
      loadingRef.current = true;
      prevHeight.current = el.scrollHeight;
      p.onLoadOlder();
    }
  };

  const toBottom = () => {
    const el = ref.current;
    if (!el) return;
    stick.current = true;
    setAway(false);
    el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
  };

  const firstIdx = useMemo(() => {
    for (const t of p.turns) if (t.entries.length) return t.entries[0].idx;
    return 0;
  }, [p.turns]);

  return (
    <div className={cx(s.listWrap, p.className)}>
      <div className={s.list} ref={ref} onScroll={onScroll} data-scroll>
        <div className={s.listInner}>
          {p.hasOlder && (
            <div className={s.loadOlder}>
              <Button size="sm" variant="ghost" loading={p.loadingOlder} onClick={p.onLoadOlder}>
                加载更早的消息
              </Button>
            </div>
          )}
          {!p.hasOlder && firstIdx > 0 && p.base !== undefined && firstIdx <= p.base && (
            <Divider>已压缩 {p.base} 条更早的消息</Divider>
          )}
          {p.turns.length === 0 && p.empty}
          {p.turns.map((t, i) => (
            <section key={t.turn + ":" + i} className={s.turn} data-turn={t.turn} data-live={t.live || undefined}>
              {p.slot?.("turn.before", t)}
              {t.blocks.map((b) => renderBlock(b, t, p))}
              {p.tail?.(t, i, p.turns)}
              {p.slot?.("turn.after", t)}
            </section>
          ))}
          {p.footer}
        </div>
      </div>
      {away && (
        <button type="button" className={s.toBottom} onClick={toBottom}>
          <ArrowDown size={14} /> 新内容
        </button>
      )}
    </div>
  );
}

function renderBlock(b: Block, t: Turn, p: MessageListProps): ReactNode {
  switch (b.kind) {
    case "user":
      return <UserMessage key={b.entry.id} entry={b.entry} actions={p.actionsFor?.(b, t)} after={p.slot?.("user.after", t, b)} />;
    case "thinking":
      return <Thinking key={b.key} text={b.text} live={b.live} startedAt={b.startedAt} />;
    case "text":
      return (
        <AssistantMessage
          key={b.key}
          entryId={b.entry?.id}
          text={b.text}
          streaming={b.streaming}
          interrupted={b.interrupted}
          actions={b.streaming ? undefined : p.actionsFor?.(b, t)}
          after={b.streaming ? undefined : p.slot?.("assistant.after", t, b)}
        />
      );
    case "tools":
      return <ToolSteps key={b.key} tools={b.tools} views={p.toolView} actionsFor={p.toolActions} />;
    case "compaction":
      return (
        <Divider key={b.entry.id}>
          已压缩到第 {b.entry.item.upto ?? 0} 条
        </Divider>
      );
    case "context":
      if (!p.showContext) return null;
      return (
        <div key={b.entry.id} className={s.contextNote}>
          {b.entry.item.source === "snapshot" ? "上下文快照" : "状态更新"}：{b.entry.item.text}
        </div>
      );
  }
}
