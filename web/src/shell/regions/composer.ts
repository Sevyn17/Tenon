// 输入框的宿主逻辑（前端方案第九节）：附件上传（artifact.put → MediaRef）、输入历史（空输入框按 ↑ / ↓）、
// "@" 提及（MentionProvider 注册表合并）。L2 的 Composer 只管呈现，这些都经 props 接进去。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Part } from "../../client";
import type { Attachment, MentionItem, UIEntry } from "../../ui";
import { useCommandContext, useRegistry, useShell, whenState } from "../context";

/** artifact.put 单次上限（appapi.MaxArtifactPut = 2 MiB）；更大的走 HTTP 上传（能力 upload），上限 MAX_UPLOAD。 */
export const MAX_ATTACHMENT = 2 << 20;
/** HTTP 上传的单文件上限（appapi.MaxUpload = 64 MiB）。 */
export const MAX_UPLOAD = 64 << 20;

interface Held extends Attachment {
  part?: Part;
}

function readDataURL(f: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(r.error ?? new Error("读取文件失败"));
    r.readAsDataURL(f);
  });
}

/** 附件：选中 / 粘贴 / 拖入即上传，发送时变成 media 部件。预览用 data URL（CSP 的 img-src 只放行 self 与 data:）。
 * target 是会话 id，或（草稿态）一个按需建会话并返回 id 的函数：上传要会话 id，第一个附件进来时才建。 */
export function useAttachments(target: string | (() => Promise<string>)) {
  const sh = useShell();
  const [items, setItems] = useState<Held[]>([]);
  const seq = useRef(0);

  const patch = (id: string, f: (a: Held) => Held) => setItems((xs) => xs.map((a) => (a.id === id ? f(a) : a)));

  const add = useCallback(
    (files: File[]) => {
      for (const f of files) {
        const id = `att${++seq.current}`;
        const mime = f.type || "application/octet-stream";
        const base: Held = { id, name: f.name || "粘贴的文件", mime, size: f.size, state: "uploading" };
        const big = f.size > MAX_ATTACHMENT;
        const limit = sh.client.can("upload") ? MAX_UPLOAD : MAX_ATTACHMENT;
        if (f.size > limit) {
          setItems((xs) => [...xs, { ...base, state: "failed", error: `超过 ${limit >> 20} MiB 上限` }]);
          continue;
        }
        setItems((xs) => [...xs, base]);
        void (async () => {
          try {
            const session = typeof target === "string" ? target : await target();
            let ref: string;
            if (big) {
              if (mime.startsWith("image/") && f.size <= 8 * MAX_ATTACHMENT) {
                const url = await readDataURL(f);
                patch(id, (a) => ({ ...a, preview: url }));
              }
              ref = await sh.client.upload(session, f);
            } else {
              const url = await readDataURL(f);
              if (mime.startsWith("image/")) patch(id, (a) => ({ ...a, preview: url }));
              ref = (await sh.client.call("artifact.put", { session, mime, base64: url.slice(url.indexOf(",") + 1) })).ref;
            }
            patch(id, (a) => ({ ...a, state: "ready", part: { kind: "media", media: { ref, mime, size: f.size } } }));
          } catch (e) {
            patch(id, (a) => ({ ...a, state: "failed", error: e instanceof Error ? e.message : String(e) }));
          }
        })();
      }
    },
    [sh.client, target],
  );

  /** 超长粘贴：折叠成"粘贴的文本"chip，不上传，发送时作为 text 部件。返回 true 表示收下。 */
  const addText = useCallback((text: string) => {
    const id = `att${++seq.current}`;
    const lines = text.split("\n").length;
    setItems((xs) => [
      ...xs,
      { id, name: `粘贴的文本（${lines} 行）`, mime: "text/plain", size: text.length, state: "ready", part: { kind: "text", text } },
    ]);
    return true;
  }, []);

  const remove = (id: string) => setItems((xs) => xs.filter((a) => a.id !== id));
  const parts = items.filter((a) => a.state === "ready" && a.part).map((a) => a.part!);
  const clear = () => setItems([]);
  const view: Attachment[] = items.map(({ part: _p, ...a }) => a);
  return { items: view, parts, add, addText, remove, clear };
}

/** 输入历史：本会话用户说过的话（新的在后）。空输入框按 ↑ 往前翻、↓ 往后翻，翻过最新一条回到空。 */
export function useHistory(entries: UIEntry[]) {
  const texts = useMemo(
    () =>
      entries
        .filter((e) => e.item.kind === "user")
        .map((e) => (e.item.content ?? []).map((p) => p.text ?? "").join(""))
        .filter((t) => t.trim() !== ""),
    [entries],
  );
  const pos = useRef<number | null>(null);
  const step = (dir: -1 | 1): string | undefined => {
    if (!texts.length) return undefined;
    const cur = pos.current ?? texts.length;
    const next = Math.min(Math.max(cur + dir, 0), texts.length);
    if (next === cur) return undefined;
    pos.current = next === texts.length ? null : next;
    return next === texts.length ? "" : texts[next];
  };
  const reset = () => (pos.current = null);
  return { step, reset };
}

/** "@" 候选：合并各 MentionProvider（when 不满足的不出现，停用的给原因）。异步来源按（来源, 查询）缓存：
 * 第一次查询先给其余来源的结果，异步结果到了换一个新的函数引用让输入框重算；缓存只留最近 MENTION_CACHE 个查询。 */
const MENTION_CACHE = 32;

export function useMentions(): ((q: string) => MentionItem[]) | undefined {
  const sh = useShell();
  const providers = useRegistry(sh.reg.mentions);
  const cx0 = useCommandContext();
  const cache = useRef(new Map<string, MentionItem[] | "pending">());
  const [ver, setVer] = useState(0);
  useEffect(() => cache.current.clear(), [providers, cx0.session]);
  return useMemo(() => {
    if (!providers.length) return undefined;
    return (q: string) => {
      const out: MentionItem[] = [];
      for (const p of providers) {
        const w = whenState(p.when?.(cx0));
        if (!w.show) continue;
        const fail = (e: unknown) => p.pack && sh.packs.fail(p.pack, e instanceof Error ? e.message : String(e));
        let xs: MentionItem[] = [];
        const key = `${p.id}|${q}`;
        const hit = cache.current.get(key);
        if (hit === "pending") continue;
        if (hit) xs = hit;
        else {
          let r: MentionItem[] | Promise<MentionItem[]>;
          try {
            r = p.suggest(q, cx0);
          } catch (e) {
            fail(e);
            continue;
          }
          if (r instanceof Promise) {
            cache.current.set(key, "pending");
            r.then(
              (v) => cache.current.set(key, v),
              (e) => {
                cache.current.set(key, []);
                fail(e);
              },
            ).finally(() => {
              while (cache.current.size > MENTION_CACHE) cache.current.delete(cache.current.keys().next().value!);
              setVer((n) => n + 1);
            });
            continue;
          }
          xs = r;
        }
        for (const x of xs) out.push({ ...x, group: x.group || p.group, disabled: x.disabled || w.disabled, reason: x.reason ?? w.reason });
      }
      return out;
    };
  }, [providers, cx0.session, sh.packs, ver]); // eslint-disable-line react-hooks/exhaustive-deps
}
