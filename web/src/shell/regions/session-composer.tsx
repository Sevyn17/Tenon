// 会话输入框的编排（首页与对话页共用）：草稿、附件、"@" 提及、"+" / "/" 菜单、输入历史、外部插入与聚焦、发送、
// 四周插槽。L2 的 Composer 只管呈现；这里把注册表与协议接上，返回一份 ComposerProps。
//
// 目标两种：session 为会话 id（对话页），或 null（首页草稿：会话还没建）。草稿态发第一条消息时才 session.create，
// 建好后先应用工具栏里选的模型与档位（sh.draft），再发消息、打开会话；第一个附件进来时也会先建会话（上传要会话 id）。
// 对话页特有的部分（运行中、排队、交互接管、状态行）由调用方在返回的 props 上再补。

import { useCallback, useEffect, useRef, useState } from "react";
import type { SessionStore } from "../../client";
import { useStore } from "../../client/react";
import type { ComposerItem, ComposerProps, UIEntry } from "../../ui";
import { Slot, useCommandContext, useRegistry, useShell, whenState } from "../context";
import type { ShellState } from "../context";
import { useAttachments, useHistory, useMentions } from "./composer";

const NO_ENTRIES: UIEntry[] = [];

export interface SessionComposerOptions {
  /** 会话 id；null = 首页草稿（还没建会话）。 */
  session: string | null;
  /** 对话页的会话 store（发消息用）；不给时按 session 从 client 取。 */
  store?: SessionStore;
  /** 会话条目（输入历史 ↑ / ↓ 用）。 */
  entries?: UIEntry[];
}

export interface SessionComposer {
  props: ComposerProps;
  setDraft: (v: string | ((x: string) => string)) => void;
  focus: () => void;
}

export function useSessionComposer(o: SessionComposerOptions): SessionComposer {
  const sh = useShell();
  const prefs = useStore(sh.prefs);
  const { session } = o;
  const [draft, setDraft] = useDraft(session ?? "new");
  const [busy, setBusy] = useState(false);
  const [focusSignal, setFocusSignal] = useState(0);
  const focus = useCallback(() => setFocusSignal((n) => n + 1), []);

  // 草稿态：按需建会话（只建一次）。
  const created = useRef<Promise<string> | null>(null);
  const ensureSession = useCallback(() => {
    if (!created.current) {
      created.current = createDraftSession(sh).catch((e) => {
        created.current = null;
        throw e;
      });
    }
    return created.current;
  }, [sh]);

  const atts = useAttachments(session ?? ensureSession);
  const history = useHistory(o.entries ?? NO_ENTRIES);
  const mentions = useMentions();
  const items = useComposerItems(session, setDraft, atts.add);

  useEffect(
    () =>
      sh.bus.on("insert", (d) => {
        if (d.session === "*" || d.session === session) setDraft((v) => (v ? v + (v.endsWith(" ") ? "" : " ") + d.text : d.text));
      }),
    [sh.bus, session, setDraft],
  );
  useEffect(() => sh.bus.on("focusComposer", focus), [sh.bus, focus]);

  const send = async () => {
    const text = draft.trim();
    const media = atts.parts;
    if ((!text && !media.length) || busy) return;
    const parts = media.length ? [...media, ...(text ? [{ kind: "text", text }] : [])] : undefined;
    if (session) {
      setDraft("");
      atts.clear();
      history.reset();
      try {
        await (o.store ?? sh.client.session(session)).send(text, parts);
      } catch (e) {
        sh.api.toast(e instanceof Error ? e.message : String(e), "danger");
      }
      return;
    }
    setBusy(true);
    try {
      const id = await ensureSession();
      setDraft("");
      atts.clear();
      sh.api.openSession(id);
      await sh.client.session(id).send(text, parts);
    } catch (e) {
      sh.api.toast(e instanceof Error ? e.message : String(e), "danger");
    } finally {
      setBusy(false);
    }
  };

  const slot = { session };
  return {
    setDraft,
    focus,
    props: {
      value: draft,
      onChange: setDraft,
      onSend: () => void send(),
      busy,
      placeholder: sh.t("conv.placeholder"),
      sendKey: prefs.sendKey,
      items,
      mentions,
      attachments: atts.items,
      onAttach: atts.add,
      onRemoveAttachment: atts.remove,
      onLongPaste: atts.addText,
      onHistory: history.step,
      above: <Slot at="composer.above" props={slot} />,
      toolbarLeft: <Slot at="composer.toolbar.left" props={slot} />,
      toolbarRight: <Slot at="composer.toolbar.right" props={slot} />,
      below: <Slot at="composer.below" props={slot} />,
      focusSignal,
    },
  };
}

/** 建草稿会话并应用工具栏里选的模型与档位（失败只提示，不挡住发消息）；清空草稿设置。 */
async function createDraftSession(sh: ShellState): Promise<string> {
  const snap = await sh.client.call("session.create", {});
  sh.client.sessions.upsert({ id: snap.id, title: snap.title, preview: "", run_state: snap.run_state, origin: snap.origin, updated_at: Date.now() });
  const d = sh.draft.get();
  sh.draft.set({});
  const fail = (what: string) => (e: unknown) => sh.api.toast(`${what}没有生效：${e instanceof Error ? e.message : String(e)}`, "warning");
  if (d.model) await sh.client.call("session.settings", { session: snap.id, model: d.model }).catch(fail("模型"));
  if (d.mode) await sh.client.call("mode.set", { session: snap.id, mode: d.mode, confirm: d.modeConfirmed || undefined }).catch(fail("档位"));
  return snap.id;
}

/** 草稿按目标保存在 sessionStorage（刷新不丢，切会话各自保留）；首页草稿的键是 "new"。 */
function useDraft(key: string): [string, (v: string | ((x: string) => string)) => void] {
  const k = `tenon.draft.${key}`;
  const [v, setV] = useState(() => {
    try {
      return sessionStorage.getItem(k) ?? "";
    } catch {
      return "";
    }
  });
  const set = useCallback(
    (x: string | ((y: string) => string)) =>
      setV((cur) => {
        const nv = typeof x === "function" ? x(cur) : x;
        try {
          if (nv) sessionStorage.setItem(k, nv);
          else sessionStorage.removeItem(k);
        } catch {
          /* 存储不可用 */
        }
        return nv;
      }),
    [k],
  );
  return [v, set];
}

/** "+" 菜单与 / 命令：ComposerAction 注册表 + 声明了 slash 的命令。草稿态下需要会话的命令按各自的 when 停用。 */
function useComposerItems(session: string | null, setDraft: (v: string | ((x: string) => string)) => void, attach: (f: File[]) => void): ComposerItem[] {
  const sh = useShell();
  const acts = useRegistry(sh.reg.composerActions);
  const cmds = useRegistry(sh.reg.commands);
  const cx0 = useCommandContext();
  const cx = { ...cx0, session };
  const insert = (t: string) => setDraft((v) => (v ? v + t : t));
  const out: ComposerItem[] = [];
  for (const a of acts) {
    const w = whenState(a.when?.(cx));
    if (!w.show) continue;
    out.push({
      id: a.id,
      group: a.group,
      title: a.title,
      description: a.description,
      icon: a.icon,
      slash: a.slash,
      keywords: a.keywords,
      disabled: w.disabled,
      reason: w.reason,
      run: () => a.run({ ...cx, insert, attach }),
    });
  }
  for (const c of cmds) {
    if (!c.slash || out.some((x) => x.slash === c.slash)) continue;
    const w = whenState(c.when?.(cx));
    if (!w.show) continue;
    out.push({
      id: "cmd:" + c.id,
      group: "command",
      title: c.title,
      description: c.description,
      icon: c.icon,
      slash: c.slash,
      keywords: c.keywords,
      disabled: w.disabled,
      reason: w.reason,
      run: async () => {
        setDraft("");
        await c.run(cx);
      },
    });
  }
  return out;
}
