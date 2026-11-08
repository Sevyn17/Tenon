// ③ 对话区（前端方案第五、六之二、七至十节）：头部、消息流、回合尾巴、输入框与交互接管、④ 对话内小卡片。
// 数据全部来自 @tenon/client 的 SessionStore 与 InteractionsStore；呈现用 @tenon/ui 的会话组件；
// 可插入的位置都经插槽与注册表（消息操作、工具渲染、交互渲染、输入框动作、尾巴建议）。

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Menu as Menu2, MoreHorizontal, Square } from "lucide-react";
import type { Entry, Interaction, ToolState } from "../../client";
import { useInteractions, useSession, useStore } from "../../client/react";
import {
  Composer,
  Dot,
  ErrorBoundary,
  FindBar,
  IconButton,
  SelectionToolbar,
  TurnNav,
  jumpTurn,
  InteractionCard,
  MarkdownExtensions,
  Menu,
  MessageList,
  TurnTail,
  buildTurns,
  cx,
  type Block,
  type ToolView,
  type Turn,
  type TurnStats,
  type UIInteraction,
  type UIResponse,
  type UITool,
} from "../../ui";
import { isVisible } from "../layout/reducer";
import { PanelsMenu } from "../layout/panelmenu";
import { Slot, useChainSlot, useCommandContext, useRegistry, useShell } from "../context";
import { toUIActions, type ActionTarget } from "../contributions";
import { useMenuGroups } from "./sidebar";
import { useSessionComposer } from "./session-composer";
import { useViewport } from "../viewport";
import s from "./conversation.module.css";

/** 主区：当前会话的对话，或某个主区页面。 */
export function MainArea() {
  const sh = useShell();
  const nav = useStore(sh.nav);
  const pages = useRegistry(sh.reg.pages);
  if (nav.page) {
    const p = pages.find((x) => x.id === nav.page);
    if (p)
      return (
        <main className={s.page}>
          <header className={s.head}>
            <span className={s.title}>{p.title}</span>
            <span className={s.grow} />
            <IconButton label={sh.t("conv.back")} size="sm" onClick={() => sh.api.openPage(null)}>
              ✕
            </IconButton>
          </header>
          <div className={s.pageBody}>{p.render()}</div>
        </main>
      );
  }
  if (!nav.session) return <EmptyMain />;
  return <Conversation key={nav.session} id={nav.session} />;
}

/** 首页：草稿会话。输入框与对话页是同一份编排（useSessionComposer），发第一条消息时才建会话。 */
function EmptyMain() {
  const sh = useShell();
  const vp = useViewport();
  const c = useSessionComposer({ session: null });
  return (
    <main className={s.conv}>
      {vp === "sm" && (
        <header className={s.head}>
          <IconButton label={sh.t("sidebar.label")} size="sm" onClick={() => sh.api.run("shell.toggleSidebar")}>
            <Menu2 size={16} />
          </IconButton>
          <span className={s.title}>{sh.brand.name}</span>
        </header>
      )}
      <div className={s.body}>
        <Hero
          onPick={(t) => {
            c.setDraft(t);
            c.focus();
          }}
        />
      </div>
      <div className={s.composerWrap}>
        <Composer {...c.props} autoFocus />
      </div>
    </main>
  );
}

function Hero({ onPick }: { onPick: (t: string) => void }) {
  const sh = useShell();
  return (
    <Slot
      at="conversation.empty"
      kind="single"
      fallback={
        <div className={s.hero}>
          <div className={s.heroTitle}>{sh.brand.greeting ?? sh.t("conv.empty.title")}</div>
          <div className={s.heroHint}>{sh.t("conv.empty.hint")}</div>
          {sh.brand.examples && sh.brand.examples.length > 0 && (
            <div className={s.examples}>
              {sh.brand.examples.map((e) => (
                <button key={e} type="button" className={s.example} onClick={() => onPick(e)}>
                  {e}
                </button>
              ))}
            </div>
          )}
        </div>
      }
    />
  );
}

// ---- 工具：客户端状态 → 组件状态 ----

function toUITool(t: ToolState): UITool {
  const phase: UITool["phase"] =
    t.phase === "ended" ? (t.status === "ok" ? "ok" : t.status === "cancelled" ? "cancelled" : "error") : t.phase;
  return { call_id: t.call_id, name: t.name, args: t.args, phase, output: t.preview, output_ref: t.output_ref, live: t.phase === "running" ? t.live : undefined, startedAt: t.startedAt, endedAt: t.endedAt };
}

/** 回合统计：耗时、步数、工具数、tokens（来自回合内 assistant 条目的 usage）。 */
function statsOf(t: Turn): TurnStats | undefined {
  const es = t.entries;
  if (!es.length) return undefined;
  let input = 0,
    output = 0,
    cacheRead = 0,
    steps = 0,
    tools = 0;
  let model = "";
  for (const e of es) {
    if (e.item.kind !== "assistant") continue;
    steps++;
    tools += e.item.tool_calls?.length ?? 0;
    if (e.item.usage) {
      input += e.item.usage.input;
      output += e.item.usage.output;
      cacheRead += e.item.usage.cache_read;
    }
    if (e.item.model) model = e.item.model;
  }
  if (!steps) return undefined;
  return { durationMs: Math.max(0, es[es.length - 1].at - es[0].at), steps, tools, input, output, cacheRead, model };
}

/** 后续建议：只问最后一个已结束的回合（FollowupProvider 注册表合并、去重，最多 4 条）；回合中或没有来源时为空。 */
function useFollowups(session: string, turn: number | null, sig = ""): string[] {
  const sh = useShell();
  const providers = useRegistry(sh.reg.followups);
  const [items, setItems] = useState<string[]>([]);
  useEffect(() => {
    setItems([]);
    if (turn === null || !providers.length) return;
    let live = true;
    const cx = { client: sh.client, session, shell: sh.api, turn };
    void Promise.all(
      providers.map(async (p) => {
        try {
          return await p.suggest(cx);
        } catch (e) {
          if (p.pack) sh.packs.fail(p.pack, e instanceof Error ? e.message : String(e));
          return [];
        }
      }),
    ).then((rs) => {
      if (!live) return;
      setItems([...new Set(rs.flat().map((x) => x.trim()).filter(Boolean))].slice(0, 4));
    });
    return () => {
      live = false;
    };
  }, [providers, session, turn, sig, sh]);
  return items;
}

// ---- 对话 ----

function Conversation({ id }: { id: string }) {
  const sh = useShell();
  const prefs = useStore(sh.prefs);
  const [st, store] = useSession(id, { create: true });
  const inter = useInteractions();
  const cmd = useCommandContext();
  const actions = useRegistry(sh.reg.messageActions);
  const renderers = useRegistry(sh.reg.toolRenderers);
  const mdExts = useRegistry(sh.reg.markdown);
  const irs = useRegistry(sh.reg.interactionRenderers);
  const entryRs = useRegistry(sh.reg.entryRenderers);
  const [ix, setIx] = useState(0);

  const pending = useMemo(
    () => inter.open.filter((x) => x.session === id || x.session.startsWith(id + "/")),
    [inter.open, id],
  );
  const awaiting = useMemo(() => {
    const ids = new Set<string>();
    for (const it of pending) {
      const callId = (it as Interaction & { approval?: { call_id?: string } }).approval?.call_id;
      if (callId) ids.add(callId);
    }
    return ids;
  }, [pending]);

  const turns = useMemo(
    () =>
      buildTurns({
        entries: st.entries,
        streams: st.streams,
        tools: st.tools.map(toUITool),
        liveTurn: st.turn?.turn ?? (st.runState === "running" ? (st.entries[st.entries.length - 1]?.turn ?? 0) + 1 : null),
        awaiting,
      }),
    [st.entries, st.streams, st.tools, st.turn, st.runState, awaiting],
  );

  // 对话内查找（⌘F）、上 / 下一回合（⌥↑ / ⌥↓）：命令经事件通道发到当前对话。
  const bodyRef = useRef<HTMLDivElement>(null);
  const [finding, setFinding] = useState(false);
  const [findSig, setFindSig] = useState(0);
  useEffect(
    () =>
      sh.bus.on("find", () => {
        setFinding(true);
        setFindSig((n) => n + 1);
      }),
    [sh.bus],
  );
  useEffect(() => sh.bus.on("jumpTurn", (d) => jumpTurn(bodyRef.current, d.dir)), [sh.bus]);

  const toolView = (name: string): ToolView | undefined => {
    for (const r of renderers) {
      if (r.tool === name || (r.tool.endsWith("*") && name.startsWith(r.tool.slice(0, -1)))) return r.view;
    }
    return undefined;
  };

  const actionsFor = (b: Block, t: Turn) => {
    const target: ActionTarget = b.kind === "user" ? "user" : "assistant";
    const entry = b.kind === "user" ? b.entry : b.kind === "text" ? b.entry : undefined;
    const text = b.kind === "user" ? (b.entry.item.content ?? []).map((p) => p.text ?? "").join("") : b.kind === "text" ? b.text : "";
    return toUIActions(actions, { ...cmd, session: id, target, entryId: entry?.id, turn: t.turn, text });
  };

  // 回合导航条的刻度：每个有用户输入的回合一个，悬停显示首句。
  const marks = useMemo(
    () =>
      turns.flatMap((t) => {
        const u = t.blocks.find((b) => b.kind === "user");
        if (!u || u.kind !== "user") return [];
        const text = (u.entry.item.content ?? []).map((p) => p.text ?? "").join("").trim();
        return [{ turn: t.turn, label: text.split("\n")[0].slice(0, 80) || sh.t("conv.turn", { n: t.turn }) }];
      }),
    [turns],
  );
  const selectionActions = (text: string) => toUIActions(actions, { ...cmd, session: id, target: "selection", text });

  const c = useSessionComposer({ session: id, store, entries: st.entries });
  const setDraft = c.setDraft;

  // 回合被拒（turn.rejected）：把那几条输入放回输入框并说明原因，不让用户的话丢掉。
  const rejected = st.rejected;
  useEffect(() => {
    if (!rejected) return;
    const ids = new Set(rejected.inputs);
    const back = store.get().outbox.filter((o) => ids.has(o.client_id));
    const text = back.map((o) => o.text).filter(Boolean).join("\n\n");
    for (const o of back) store.discard(o.client_id);
    if (text) setDraft((v) => (v ? text + "\n\n" + v : text));
    sh.api.toast(sh.t("conv.rejected", { reason: rejected.reason || sh.t("conv.rejected.default") }) + (text ? sh.t("conv.rejected.back") : ""), "warning");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rejected]);


  // blocking = false 的交互（智能体不停下来等）内联在消息流末尾，不接管输入框。
  const blocking = pending.filter((x) => x.blocking !== false);
  const inline = pending.filter((x) => x.blocking === false);
  const respond = (it: Interaction) => async (r: UIResponse) => {
    await sh.client.interactions.respond(it.id, r);
  };
  const cur = blocking[Math.min(ix, Math.max(0, blocking.length - 1))];
  const takeover = cur ? (
    <InteractionCard
      key={cur.id}
      it={cur as UIInteraction}
      keyboard
      position={
        blocking.length > 1
          ? {
              index: blocking.indexOf(cur),
              total: blocking.length,
              prev: () => setIx((i) => Math.max(0, i - 1)),
              next: () => setIx((i) => Math.min(blocking.length - 1, i + 1)),
            }
          : undefined
      }
      source={cur.session !== id ? sh.t("conv.subSource", { sub: cur.session.slice(id.length + 1) }) : undefined}
      body={irs.find((r) => r.kind === cur.kind)?.render(cur)}
      onRespond={respond(cur)}
      onDenyWithReason={async (text) => {
        await sh.client.interactions.respond(cur.id, { approve: false });
        if (text.trim()) await store.send(text.trim());
      }}
    />
  ) : null;

  const chainTakeover = useChainSlot("composer.takeover", { session: id });
  const lastIndex = turns.length - 1;
  const lastDone = turns.length && !turns[lastIndex].live && st.runState !== "running" ? turns[lastIndex].turn : null;
  // 签名：回合结束事件可能先于最后一条助手条目到达，条目数变了再问一次。
  const followups = useFollowups(id, lastDone, lastDone === null ? "" : String(turns[lastIndex].entries.length));
  // 已结束回合只在这些变了时重渲染（其余靠回合自身内容的签名）。
  const revision = useMemo(
    () => ({ showContext: prefs.showContext, actions, renderers, lastEnd: st.lastEnd, followups, entryRs }),
    [prefs.showContext, actions, renderers, st.lastEnd, followups, entryRs],
  );
  const content = (
    <MessageList
      turns={turns}
      base={st.base}
      hasOlder={st.entries.length > 0 && st.entries[0].idx > 0}
      loadingOlder={st.loadingOlder}
      onLoadOlder={() => void store.loadOlder()}
      actionsFor={actionsFor}
      toolView={toolView}
      entryView={(e, t) => {
        const r = entryRs.find((x) => x.kind === e.item.kind);
        return r ? r.render(e as Entry, { ...cmd, session: id, turn: t.turn }) : undefined;
      }}
      showContext={prefs.showContext}
      tick={st.streams.map((x) => x.text.length).join(",") + "|" + st.tools.length}
      revision={revision}
      empty={st.status === "ready" ? <Hero onPick={(t) => setDraft(t)} /> : null}
      slot={(at, t) => <Slot at={`message.${at}`} props={{ session: id, turn: t.turn }} />}
      tail={(t, i) => {
        if (t.live) return null;
        const isLast = i === lastIndex;
        return (
          <TurnTail
            stats={statsOf(t)}
            onOpenStats={() => sh.api.openPanel("usage", { turn: t.turn })}
            actions={toUIActions(actions, { ...cmd, session: id, target: "turn", turn: t.turn })}
            end={isLast && st.lastEnd && st.lastEnd.kind !== "done" ? st.lastEnd : null}
            followups={isLast ? followups : undefined}
            onFollowup={(text) => {
              setDraft(text);
              sh.api.focusComposer();
            }}
            onContinue={() => void store.send(sh.t("conv.continue"))}
            extra={<Slot at="turn.tail" props={{ session: id, turn: t.turn, last: isLast }} />}
          />
        );
      }}
    />
  );

  return (
    <main className={s.conv}>
      <Header id={id} title={st.title} />
      {st.closed && <div className={cx(s.banner, s.banner_danger)}>{sh.t("conv.closed", { reason: st.closed })}</div>}
      {st.error && st.status === "error" && <div className={cx(s.banner, s.banner_danger)}>{st.error}</div>}
      <Slot at="conversation.banner" props={{ session: id }} />
      <div ref={bodyRef} className={s.body} style={{ ["--conv-width" as string]: `${prefs.chatWidth}px` }}>
        {finding && <FindBar root={bodyRef} focusSignal={findSig} onClose={() => setFinding(false)} />}
        <MarkdownExtensions value={mdExts.map((m) => m.ext)}>{content}</MarkdownExtensions>
        {inline.length > 0 && (
          <div className={s.inlineInteractions}>
            {inline.map((it) => (
              <InteractionCard
                key={it.id}
                it={it as UIInteraction}
                source={it.session !== id ? sh.t("conv.subSource", { sub: it.session.slice(id.length + 1) }) : undefined}
                body={irs.find((r) => r.kind === it.kind)?.render(it)}
                onRespond={respond(it)}
              />
            ))}
          </div>
        )}
        <InlineCards session={id} />
        <TurnNav root={bodyRef} marks={marks} />
        <SelectionToolbar root={bodyRef} actions={selectionActions} />
      </div>
      <div className={s.composerWrap}>
        <Composer
          {...c.props}
          onStop={() => void store.cancel()}
          running={st.runState === "running"}
          disabled={!!st.closed}
          disabledReason={st.closed ? sh.t("conv.closed", { reason: st.closed }) : undefined}
          placeholder={st.runState === "running" ? sh.t("conv.placeholder.running") : sh.t("conv.placeholder")}
          queued={st.outbox
            .filter((o) => o.state !== "sent")
            .map((o) => ({ id: o.client_id, text: o.text, state: o.state === "failed" ? "failed" : "sending", error: o.error }))}
          onRetryQueued={(cid) => void store.retry(cid)}
          onDiscardQueued={(cid) => store.discard(cid)}
          status={st.runState === "running" ? <RunningStatus startedAt={st.turn?.startedAt} onStop={() => void store.cancel()} /> : null}
          takeover={takeover ?? chainTakeover}
          autoFocus
        />
      </div>
    </main>
  );
}

function RunningStatus({ startedAt, onStop }: { startedAt?: number; onStop: () => void }) {
  const sh = useShell();
  const [, tick] = useState(0);
  useEffect(() => {
    const t = setInterval(() => tick((x) => x + 1), 1000);
    return () => clearInterval(t);
  }, []);
  const sec = startedAt ? Math.floor((Date.now() - startedAt) / 1000) : 0;
  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
      <Dot state="running" /> {sh.t("conv.working")} · {Math.floor(sec / 60)}:{String(sec % 60).padStart(2, "0")}
      <IconButton label={sh.t("conv.stop")} size="sm" onClick={onStop}>
        <Square size={12} />
      </IconButton>
    </span>
  );
}

function Header({ id, title }: { id: string; title: string }) {
  const sh = useShell();
  const vp = useViewport();
  const groups = useMenuGroups("conversation.header.menu");
  // 点标题直接改名：回车或失焦保存，Esc 放弃。标题为空（还没自动起名）时编辑框也从空开始。
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const done = useRef(false);
  const inputRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (editing) inputRef.current?.select();
  }, [editing]);
  const begin = () => {
    done.current = false;
    setDraft(title);
    setEditing(true);
  };
  // 回车、失焦都走这里，只认第一次（回车后输入框卸载还会再触发一次失焦）。
  const finish = async (keep: boolean) => {
    if (done.current) return;
    done.current = true;
    setEditing(false);
    if (!keep) return;
    const next = draft.trim();
    if (!next || next === title) return;
    try {
      await sh.client.call("session.update", { session: id, title: next });
      // 列表那一行只改标题（其余字段保持原样）；会话自己的标题由 session.updated 事件更新。
      const row = sh.client.sessions.find(id);
      if (row) sh.client.sessions.upsert({ ...row, title: next });
    } catch (e) {
      sh.api.toast(e instanceof Error ? e.message : String(e), "danger");
    }
  };

  return (
    <header className={s.head}>
      {vp === "sm" && (
        <IconButton label={sh.t("sidebar.label")} size="sm" onClick={() => sh.api.run("shell.toggleSidebar")}>
          <Menu2 size={16} />
        </IconButton>
      )}
      <Slot at="conversation.header.leading" props={{ session: id }} />
      {editing ? (
        <input
          ref={inputRef}
          type="text"
          className={s.titleInput}
          value={draft}
          placeholder={sh.t("sessions.untitled")}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={() => void finish(true)}
          onKeyDown={(e) => {
            if (e.nativeEvent.isComposing) return;
            if (e.key === "Enter") {
              e.preventDefault();
              void finish(true);
            } else if (e.key === "Escape") {
              e.preventDefault();
              e.stopPropagation();
              void finish(false);
            }
          }}
          aria-label={sh.t("sessions.rename")}
        />
      ) : (
        <span
          className={s.title}
          title={sh.t("sessions.rename")}
          role="button"
          tabIndex={0}
          onClick={begin}
          onKeyDown={(e) => {
            if (e.key === "Enter") begin();
          }}
        >
          {title || sh.t("sessions.untitled")}
        </span>
      )}
      <span className={s.grow} />
      <Slot at="conversation.header.actions" props={{ session: id }} />
      <PanelsMenu />
      <Menu
        placement="bottom-end"
        groups={groups}
        trigger={({ ref, toggle }) => (
          <IconButton ref={ref} label={sh.t("conv.menu")} size="sm" onClick={toggle}>
            <MoreHorizontal size={16} />
          </IconButton>
        )}
      />
    </header>
  );
}

/** ④ 对话内小卡片：面板声明了 card 且该面板没在可见处打开时显示，点了打开面板。卡片组件自己取数据，没话说时返回 null。 */
function InlineCards({ session }: { session: string }) {
  const sh = useShell();
  const panels = useRegistry(sh.reg.panels);
  const layout = useStore(sh.layout);
  return (
    <div className={s.cards}>
      {panels.map((p) => {
        if (!p.card || isVisible(layout, p.id)) return null;
        const C = p.card;
        return (
          <ErrorBoundary key={p.id} label={`卡片 ${p.title}`} fallback={() => null} onError={(e) => p.pack && sh.packs.fail(p.pack, e.message)}>
            <C session={session} open={() => sh.api.openPanel(p.id)} />
          </ErrorBoundary>
        );
      })}
      <Slot at="conversation.card" props={{ session }} />
    </div>
  );
}

/** 小卡片的外框：图标 + 标题 + 内容，整张可点。面板的 card 组件用它包住自己的内容。 */
export function PanelCard(p: { icon?: ReactNode; title: ReactNode; onOpen: () => void; tone?: "warning" | "danger"; children?: ReactNode }) {
  return (
    <button type="button" className={cx(s.card, p.tone && s[`card_${p.tone}`])} onClick={p.onOpen}>
      <div className={s.cardHead}>
        {p.icon} {p.title}
      </div>
      {p.children && <div className={s.cardBody}>{p.children}</div>}
    </button>
  );
}

