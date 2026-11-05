// 会话视图（protocol 2，docs/06 - 应用协议契约.md 10.2、10.5）。
//
// 一致性：先 session.get 取快照，再取日志尾部，再从快照的 cursor 往后 session.subscribe——快照之后的变化一定在
// cursor 之后，订阅回放不漏不重。记下最后收到的持久游标：session.lagged（背压断开）与 local.reconnected（断线重连）
// 时从它重新订阅；游标过期（cursor_expired）时整个重新加载。
//
// 流式：message.* 按 stream_id 累积、带 entry_id；entry.appended 到达后删掉 entry_id 等于条目 id 的流，以条目为准。
// 工具卡片按 call_id 对应：条目里已有的调用由条目渲染，还没落库的由 tool.* 事件渲染。turn.ended 清掉残留。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LOCAL_RECONNECTED } from "./api";
import { Markdown } from "./Markdown";
import { call, clientID, onEvent } from "./transport";
import { entryText, errCode, errReason, errText, pretty, preview, textOf } from "./util";
import type {
  CursorRef,
  Entry,
  EntryAppended,
  Envelope,
  LogCompacted,
  LogRewound,
  MessageDelta,
  MessageStarted,
  SessionSnapshot,
  SettingsChanged,
  StateChanged,
  StreamRef,
  ToolArgsDelta,
  ToolEnded,
  ToolRunning,
  ToolStarted,
} from "./wire.gen";

const PAGE = 50;

interface Stream {
  stream_id: string;
  entry_id: string;
  part: string;
  text: string;
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

export function ToolCard(props: { name: string; args: unknown; result?: string; error?: boolean; pending?: boolean; note?: string }) {
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
          {props.note && <div className="muted small">{props.note}</div>}
        </div>
      )}
    </div>
  );
}

export function Chat({ session, ask, onForked, onNotice }: ChatProps) {
  const [rows, setRows] = useState<Entry[]>([]);
  const [base, setBase] = useState(0);
  const [running, setRunning] = useState(false);
  const [maxSteps, setMaxSteps] = useState(0);
  const [streams, setStreams] = useState<Stream[]>([]);
  const [live, setLive] = useState<LiveTool[]>([]);
  const [input, setInput] = useState("");
  const [err, setErr] = useState("");
  const [loadingOld, setLoadingOld] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);
  const stick = useRef(true); // 贴底：新内容到达时滚到最后
  const cursor = useRef(0); // 最后收到的持久游标
  const gen = useRef(0); // 加载代次：会话切换或重新加载后，旧的订阅事件丢弃

  const subscribe = useCallback(
    async (after: number) => {
      try {
        await call("session.subscribe", { session, after });
      } catch (e) {
        if (errCode(e) === "cursor_expired") {
          void load(); // eslint-disable-line @typescript-eslint/no-use-before-define
          return;
        }
        setErr(errText(e));
      }
    },
    // load 在下面定义；两者互相引用，用 ref 化的 session 作依赖即可
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [session],
  );

  const applySnapshot = (s: SessionSnapshot) => {
    setRunning(s.run_state === "running");
    setMaxSteps(s.settings.max_steps);
    setBase(s.log.base);
    cursor.current = s.cursor;
    setStreams(
      (s.live?.streams ?? []).map((x) => ({ stream_id: x.stream_id, entry_id: x.entry_id, part: x.part, text: x.text })),
    );
    setLive((s.live?.tools ?? []).map((t) => ({ call_id: t.call_id, name: t.name, args: t.args ? pretty(t.args) : "" })));
  };

  const load = useCallback(async () => {
    setErr("");
    const g = ++gen.current;
    try {
      let snap: SessionSnapshot;
      try {
        snap = await call("session.get", { session });
      } catch (e) {
        if (errCode(e) !== "not_found") throw e;
        snap = await call("session.create", { session }); // 界面选中的会话还不存在：显式新建（服务端不隐式建，G1）
      }
      if (g !== gen.current) return;
      applySnapshot(snap);
      const r = await call("session.entries", { session, tail: PAGE });
      if (g !== gen.current) return;
      // 快照与日志之间提交的条目会经订阅回放补上；这里按 idx 去重
      setRows(r.entries ?? []);
      setBase(r.base);
      stick.current = true;
      await subscribe(snap.cursor);
    } catch (e) {
      setErr(errText(e));
    }
  }, [session, subscribe]);

  useEffect(() => {
    setRows([]);
    setStreams([]);
    setLive([]);
    void load();
    return () => {
      gen.current++;
      void call("session.unsubscribe", { session }).catch(() => {});
    };
  }, [load, session]);

  useEffect(
    () =>
      onEvent((ev: Envelope) => {
        if (ev.kind === LOCAL_RECONNECTED) {
          void subscribe(cursor.current);
          return;
        }
        if (ev.session !== session) return;
        if (ev.cursor && ev.cursor > cursor.current) cursor.current = ev.cursor;
        switch (ev.kind) {
          case "entry.appended": {
            const e = (ev.data as EntryAppended).entry;
            setRows((rs) => {
              if (rs.some((r) => r.idx === e.idx)) return rs.map((r) => (r.idx === e.idx ? e : r));
              return [...rs, e].sort((a, b) => a.idx - b.idx);
            });
            setStreams((ss) => ss.filter((s) => s.entry_id !== e.id));
            if (e.item.kind === "tool_result") setLive((ls) => ls.filter((l) => l.call_id !== e.item.call_id));
            break;
          }
          case "log.rewound": {
            const to = (ev.data as LogRewound).to;
            setRows((rs) => rs.filter((r) => r.idx < to));
            break;
          }
          case "log.compacted":
            setBase((ev.data as LogCompacted).upto);
            break;
          case "session.state_changed":
            setRunning((ev.data as StateChanged).run_state === "running");
            break;
          case "session.settings_changed":
            setMaxSteps((ev.data as SettingsChanged).settings.max_steps);
            break;
          case "session.lagged": // 背压断开：只丢易失事件，从最后送达的持久游标重新订阅
            setStreams([]);
            void subscribe((ev.data as CursorRef).cursor);
            break;
          case "session.closed":
            onNotice(`会话 ${session} 已关闭`);
            break;
          case "turn.started":
            setRunning(true);
            break;
          case "turn.ended":
          case "turn.rejected":
            setRunning(false);
            setStreams([]);
            setLive([]);
            break;
          case "message.started": {
            const d = ev.data as MessageStarted;
            setStreams((ss) => [...ss.filter((s) => s.stream_id !== d.stream_id), { ...d, text: "" }]);
            break;
          }
          case "message.delta": {
            const d = ev.data as MessageDelta;
            setStreams((ss) => ss.map((s) => (s.stream_id === d.stream_id ? { ...s, text: s.text + d.text } : s)));
            break;
          }
          case "message.discarded": {
            const d = ev.data as StreamRef;
            setStreams((ss) => ss.filter((s) => s.stream_id !== d.stream_id));
            break;
          }
          case "tool.started": {
            const d = ev.data as ToolStarted;
            setLive((ls) => (ls.some((l) => l.call_id === d.call_id) ? ls : [...ls, { call_id: d.call_id, name: d.name, args: "" }]));
            break;
          }
          case "tool.args_delta": {
            const d = ev.data as ToolArgsDelta;
            setLive((ls) => ls.map((l) => (l.call_id === d.call_id ? { ...l, args: l.args + d.text } : l)));
            break;
          }
          case "tool.running": {
            const d = ev.data as ToolRunning;
            setLive((ls) => ls.map((l) => (l.call_id === d.call_id ? { ...l, args: pretty(d.args) } : l)));
            break;
          }
          case "tool.ended": {
            const d = ev.data as ToolEnded;
            setLive((ls) => ls.map((l) => (l.call_id === d.call_id ? { ...l, preview: d.preview, error: d.status !== "ok" } : l)));
            break;
          }
        }
      }),
    [session, subscribe, onNotice],
  );

  useEffect(() => {
    const el = listRef.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [rows, streams, live]);

  const loadOlder = async () => {
    const first = rows[0]?.idx ?? 0;
    if (loadingOld || first <= 0) return;
    setLoadingOld(true);
    const el = listRef.current;
    const h = el?.scrollHeight ?? 0;
    try {
      const from = Math.max(first - PAGE, 0);
      const r = await call("session.entries", { session, from, to: first });
      setRows((rs) => [...(r.entries ?? []).filter((e) => !rs.some((x) => x.idx === e.idx)), ...rs]);
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

  // call_id → 工具结果条目；已落库的调用 id
  const results = useMemo(() => {
    const m = new Map<string, Entry>();
    for (const r of rows) if (r.item.kind === "tool_result" && r.item.call_id) m.set(r.item.call_id, r);
    return m;
  }, [rows]);
  const calls = useMemo(() => {
    const s = new Set<string>();
    for (const r of rows) for (const c of r.item.tool_calls ?? []) s.add(c.id);
    return s;
  }, [rows]);
  const lastTurn = rows.length ? rows[rows.length - 1].turn : 0;

  const act = async (f: () => Promise<unknown>) => {
    setErr("");
    try {
      await f();
    } catch (e) {
      if (errReason(e) === "stale") {
        onNotice("会话已被别处更新，已重新加载");
        await load();
        return;
      }
      setErr(errText(e));
    }
  };

  const send = () =>
    act(async () => {
      const text = input.trim();
      if (!text) return;
      const client_id = clientID();
      if (running) {
        try {
          await call("session.steer", { session, text, client_id });
        } catch (e) {
          if (errCode(e) !== "invalid") throw e;
          await call("session.send", { session, text, client_id }); // 回合恰好结束
        }
      } else {
        await call("session.send", { session, text, client_id });
      }
      setInput("");
      stick.current = true;
    });

  const rewind = () =>
    act(async () => {
      const v = await ask("回退", "回退到第几个回合（这一回合及之后的都去掉）", String(Math.max(lastTurn, 1)));
      if (v === null) return;
      const to_turn = Math.max(1, Math.floor(Number(v)) || 1);
      const r = await call("session.rewind", { session, to_turn, if_cursor: cursor.current });
      onNotice(`已回退到日志位置 ${r.to}`);
    });
  const compact = () =>
    act(async () => {
      const r = await call("session.compact", { session, if_cursor: cursor.current });
      onNotice(`已压缩到 ${r.upto}` + (r.tokens_before ? `（${r.tokens_before} → ${r.tokens_after} tokens）` : ""));
    });
  const fork = () =>
    act(async () => {
      const to = await ask("分叉", "新会话 id", session + "-fork");
      if (!to) return;
      const s = await call("session.fork", { session, to: to.trim(), if_cursor: cursor.current, client_id: clientID() });
      onForked(s.id);
    });
  const steps = () =>
    act(async () => {
      const v = await ask("每回合最大步数", "0 = 不限", String(maxSteps));
      if (v === null || v === "") return;
      const r = await call("session.settings", { session, max_steps: Math.max(0, Math.floor(Number(v))) });
      setMaxSteps(r.settings.max_steps);
      onNotice(`最大步数：${r.settings.max_steps || "不限"}` + (r.committed ? "" : "（回合结束后保存）"));
    });

  const firstIdx = rows[0]?.idx ?? 0;
  const reasoning = streams.filter((s) => s.part === "reasoning").map((s) => s.text).join("");
  const text = streams.filter((s) => s.part !== "reasoning").map((s) => s.text).join("");

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
        <button disabled={running} onClick={() => void fork()}>
          分叉
        </button>
        <button onClick={() => void steps()}>步数</button>
      </div>
      <div className="list" ref={listRef} onScroll={onScroll}>
        {firstIdx > 0 && (
          <div className="muted center">
            {loadingOld ? "加载中…" : <button onClick={() => void loadOlder()}>加载更早（前面还有 {firstIdx} 条）</button>}
          </div>
        )}
        {firstIdx > 0 && firstIdx <= base && <div className="muted center">— 以上已压缩 —</div>}
        {rows.map((r) => (
          <EntryView key={r.idx} entry={r} results={results} orphan={calls} />
        ))}
        {live
          .filter((l) => !calls.has(l.call_id))
          .map((l) => (
            <ToolCard key={l.call_id} name={l.name} args={l.args} result={l.preview} error={l.error} pending={l.preview === undefined} />
          ))}
        {(reasoning || text) && (
          <div className="msg assistant streaming">
            {reasoning && (
              <details className="reasoning" open>
                <summary>思考</summary>
                <div className="plain">{reasoning}</div>
              </details>
            )}
            {text && <Markdown text={text} />}
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
      const reasoning = (it.reasoning ?? []).map((r) => r.text).join("");
      return (
        <div className="msg assistant">
          {reasoning && (
            <details className="reasoning">
              <summary>思考</summary>
              <div className="plain">{reasoning}</div>
            </details>
          )}
          <Markdown text={textOf(it.content)} />
          {(it.tool_calls ?? []).map((c) => {
            const r = results.get(c.id)?.item;
            return (
              <ToolCard
                key={c.id}
                name={c.name}
                args={c.args}
                result={r ? textOf(r.output) : undefined}
                error={r ? r.status !== "ok" : false}
                pending={!r}
                note={r?.truncated ? `结果已截断${r.output_ref ? `，全文：${r.output_ref}` : ""}` : undefined}
              />
            );
          })}
          {it.interrupted && <div className="muted">（已中断）</div>}
        </div>
      );
    }
    case "tool_result": {
      if (it.call_id && orphan.has(it.call_id)) return null; // 已在对应工具卡片里显示
      return <ToolCard name={`结果 ${it.call_id ?? ""}`} args="" result={textOf(it.output)} error={it.status !== "ok"} />;
    }
    case "compaction":
      return (
        <details className="msg compaction">
          <summary>压缩摘要（到 {it.upto}）</summary>
          <Markdown text={textOf(it.summary)} />
        </details>
      );
    case "context":
      return null; // 注入给模型的状态快照，不显示
    default:
      return <div className="muted">（未知条目：{it.kind}）</div>;
  }
}
