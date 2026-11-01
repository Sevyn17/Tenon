// 会话视图：日志分页（首屏只取尾部，向上滚动加载更早）、流式文本、工具卡片、会话操作。
// 流式文本按 text_delta 批次追加；持久 entry 事件到达后以日志为准（清掉流式缓冲）。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Markdown } from "./Markdown";
import { EV, type Entry, type Envelope, type EntryEvent, type ToolCall, type ToolCallEvent, type ToolResultEvent } from "./protocol";
import { call, onEvent } from "./transport";
import { entryText, errCode, errText, pretty, preview, textOf } from "./util";

const PAGE = 50;

interface Row {
  idx: number;
  entry: Entry;
}

interface LiveTool {
  call_id: string;
  name: string;
  args: string;
  preview?: string;
  error?: boolean;
}

export interface ChatProps {
  session: string;
  ask: (title: string, label?: string, initial?: string) => Promise<string | null>;
  onForked: (id: string) => void;
  onNotice: (text: string) => void;
}

export function ToolCard(props: { name: string; args: unknown; result?: string; error?: boolean; pending?: boolean }) {
  const [open, setOpen] = useState(false);
  const status = props.pending ? "运行中" : props.error ? "失败" : "完成";
  return (
    <div className={"tool" + (props.error ? " tool-err" : "")}>
      <div className="tool-head" onClick={() => setOpen(!open)}>
        <span>{open ? "▾" : "▸"}</span> <b>{props.name}</b> <span className="muted">{status}</span>
      </div>
      {open && (
        <div className="tool-body">
          <div className="muted">参数</div>
          <pre>{preview(pretty(props.args), 4000)}</pre>
          {props.result !== undefined && (
            <>
              <div className="muted">结果（预览）</div>
              <pre>{preview(props.result, 4000)}</pre>
            </>
          )}
        </div>
      )}
    </div>
  );
}

export function Chat({ session, ask, onForked, onNotice }: ChatProps) {
  const [rows, setRows] = useState<Row[]>([]);
  const [base, setBase] = useState(0);
  const [running, setRunning] = useState(false);
  const [stream, setStream] = useState("");
  const [live, setLive] = useState<LiveTool[]>([]);
  const [input, setInput] = useState("");
  const [err, setErr] = useState("");
  const [loadingOld, setLoadingOld] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);
  const stick = useRef(true); // 贴底：新内容到达时滚到最后

  const load = useCallback(async () => {
    setErr("");
    try {
      const o = await call("session.open", { session });
      setRunning(o.run_state !== "Idle" && o.run_state !== "idle");
      const r = await call("session.entries", { session, tail: PAGE });
      setRows(r.entries ?? []);
      setBase(r.base);
      stick.current = true;
    } catch (e) {
      setErr(errText(e));
    }
  }, [session]);

  useEffect(() => {
    setRows([]);
    setStream("");
    setLive([]);
    void load();
  }, [load]);

  useEffect(
    () =>
      onEvent((ev: Envelope) => {
        if (ev.kind === EV.resync) {
          void load();
          return;
        }
        if (ev.session !== session) return;
        switch (ev.kind) {
          case EV.entry: {
            const d = ev.data as EntryEvent;
            setRows((rs) => {
              if (rs.some((r) => r.idx === d.idx)) return rs.map((r) => (r.idx === d.idx ? { idx: d.idx, entry: d.entry } : r));
              return [...rs, { idx: d.idx, entry: d.entry }].sort((a, b) => a.idx - b.idx);
            });
            if (d.entry.item.kind === "assistant") setStream("");
            if (d.entry.item.kind === "tool_result") {
              const id = d.entry.item.tool_result?.CallID;
              setLive((ls) => ls.filter((l) => l.call_id !== id));
            }
            break;
          }
          case EV.rewind: {
            const to = (ev.data as { to: number }).to;
            setRows((rs) => rs.filter((r) => r.idx < to));
            break;
          }
          case EV.turnStart:
            setRunning(true);
            break;
          case EV.turnEnd:
          case EV.turnDenied:
            setRunning(false);
            setStream("");
            setLive([]);
            break;
          case EV.textDelta:
            setStream((s) => s + (ev.data as { text: string }).text);
            break;
          case EV.textReset:
            setStream("");
            break;
          case EV.toolCall: {
            const d = ev.data as ToolCallEvent;
            setLive((ls) => [...ls.filter((l) => l.call_id !== d.call_id), { ...d }]);
            break;
          }
          case EV.toolResult: {
            const d = ev.data as ToolResultEvent;
            setLive((ls) => ls.map((l) => (l.call_id === d.call_id ? { ...l, preview: d.preview, error: d.error } : l)));
            break;
          }
        }
      }),
    [session, load],
  );

  useEffect(() => {
    const el = listRef.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [rows, stream, live]);

  const loadOlder = async () => {
    const first = rows[0]?.idx ?? 0;
    if (loadingOld || first <= 0) return;
    setLoadingOld(true);
    const el = listRef.current;
    const h = el?.scrollHeight ?? 0;
    try {
      const from = Math.max(first - PAGE, 0);
      const r = await call("session.entries", { session, from, to: first });
      setRows((rs) => [...(r.entries ?? []), ...rs]);
      requestAnimationFrame(() => {
        if (el) el.scrollTop += el.scrollHeight - h; // 保持视口位置
      });
    } catch (e) {
      setErr(errText(e));
    } finally {
      setLoadingOld(false);
    }
  };

  const onScroll = () => {
    const el = listRef.current;
    if (!el) return;
    stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    if (el.scrollTop < 60) void loadOlder();
  };

  // call_id → 工具结果条目
  const results = useMemo(() => {
    const m = new Map<string, Entry>();
    for (const r of rows) if (r.entry.item.kind === "tool_result") m.set(r.entry.item.tool_result!.CallID, r.entry);
    return m;
  }, [rows]);
  const calls = useMemo(() => {
    const s = new Set<string>();
    for (const r of rows) for (const c of r.entry.item.assistant?.ToolCalls ?? []) s.add(c.ID);
    return s;
  }, [rows]);

  const act = async (f: () => Promise<unknown>) => {
    setErr("");
    try {
      await f();
    } catch (e) {
      setErr(errText(e));
    }
  };

  const send = () =>
    act(async () => {
      const t = input.trim();
      if (!t) return;
      if (running) {
        try {
          await call("session.steer", { session, text: t });
        } catch (e) {
          if (errCode(e) !== "not_idle") throw e;
          await call("session.send", { session, text: t }); // 回合恰好结束
        }
      } else {
        await call("session.send", { session, text: t });
      }
      setInput("");
      stick.current = true;
    });

  const rewind = () =>
    act(async () => {
      const v = await ask("回退", "回退最近几个回合？", "1");
      if (v === null) return;
      const r = await call("session.rewind", { session, turns: Number(v) || 1 });
      onNotice(`已回退到日志位置 ${r.to}`);
    });
  const compact = () =>
    act(async () => {
      const r = await call("session.compact", { session });
      onNotice(`已压缩到 ${r.upto}` + (r.tokens_before ? `（${r.tokens_before} → ${r.tokens_after} tokens）` : ""));
      await load();
    });
  const fork = () =>
    act(async () => {
      const to = await ask("分叉", "新会话 id", session + "-fork");
      if (!to) return;
      const r = await call("session.fork", { session, to });
      onForked(r.id);
    });
  const maxSteps = () =>
    act(async () => {
      const cur = await call("session.max_steps", { session });
      const v = await ask("每回合最大步数", "0 = 不限", String(cur.n));
      if (v === null || v === "") return;
      const r = await call("session.max_steps", { session, n: Math.max(0, Math.floor(Number(v))) });
      onNotice(`最大步数：${r.n || "不限"}`);
    });

  const firstIdx = rows[0]?.idx ?? 0;

  return (
    <div className="chat">
      <div className="toolbar">
        <b>{session}</b>
        <span className="muted">{running ? "运行中" : "空闲"}</span>
        <span className="spacer" />
        <button disabled={!running} onClick={() => void act(() => call("session.cancel", { session }))}>
          取消
        </button>
        <button disabled={running} onClick={() => void rewind()}>
          回退
        </button>
        <button disabled={running} onClick={() => void compact()}>
          压缩
        </button>
        <button onClick={() => void fork()}>分叉</button>
        <button onClick={() => void maxSteps()}>步数</button>
      </div>
      <div className="list" ref={listRef} onScroll={onScroll}>
        {firstIdx > 0 && (
          <div className="muted center">
            {loadingOld ? "加载中…" : <button onClick={() => void loadOlder()}>加载更早（前面还有 {firstIdx} 条）</button>}
          </div>
        )}
        {firstIdx > 0 && firstIdx <= base && <div className="muted center">— 以上已压缩 —</div>}
        {rows.map((r) => (
          <EntryView key={r.idx} entry={r.entry} results={results} orphan={calls} />
        ))}
        {live.map((l) => (
          <ToolCard key={l.call_id} name={l.name} args={l.args} result={l.preview} error={l.error} pending={l.preview === undefined} />
        ))}
        {stream && (
          <div className="msg assistant streaming">
            <Markdown text={stream} />
          </div>
        )}
      </div>
      {err && <div className="error">{err}</div>}
      <div className="composer">
        <textarea
          value={input}
          placeholder={running ? "回合进行中：发送内容作为插话（steer）" : "输入消息，Ctrl+Enter 发送"}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) void send();
          }}
        />
        <button onClick={() => void send()}>{running ? "插话" : "发送"}</button>
      </div>
    </div>
  );
}

function EntryView({ entry, results, orphan }: { entry: Entry; results: Map<string, Entry>; orphan: Set<string> }) {
  const it = entry.item;
  switch (it.kind) {
    case "user":
      return (
        <div className="msg user">
          <div className="plain">{entryText(entry)}</div>
        </div>
      );
    case "assistant": {
      const a = it.assistant!;
      const reasoning = (a.Reasoning ?? []).map((r) => r.Text ?? "").join("");
      return (
        <div className="msg assistant">
          {reasoning && (
            <details className="reasoning">
              <summary>思考</summary>
              <div className="plain">{reasoning}</div>
            </details>
          )}
          <Markdown text={textOf(a.Content)} />
          {(a.ToolCalls ?? []).map((c: ToolCall) => {
            const r = results.get(c.ID)?.item.tool_result;
            return (
              <ToolCard
                key={c.ID}
                name={c.Name}
                args={c.Args}
                result={r ? textOf(r.Output) : undefined}
                error={r ? r.Status.State !== "ok" : false}
                pending={!r}
              />
            );
          })}
          {a.Interrupted && <div className="muted">（已中断）</div>}
        </div>
      );
    }
    case "tool_result": {
      const r = it.tool_result!;
      if (orphan.has(r.CallID)) return null; // 已在对应工具卡片里显示
      return <ToolCard name={`结果 ${r.CallID}`} args="" result={textOf(r.Output)} error={r.Status.State !== "ok"} />;
    }
    case "compaction":
      return (
        <details className="msg compaction">
          <summary>压缩摘要（到 {it.compaction?.Upto}）</summary>
          <Markdown text={textOf(it.compaction?.Summary)} />
        </details>
      );
    default:
      return <div className="muted">（未知条目：{it.kind}）</div>;
  }
}
