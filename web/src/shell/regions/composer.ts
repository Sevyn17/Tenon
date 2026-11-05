// 输入框的宿主逻辑（前端方案第九节）：附件上传（artifact.put → MediaRef）、输入历史（空输入框按 ↑ / ↓）、
// "@" 提及（MentionProvider 注册表合并）。L2 的 Composer 只管呈现，这些都经 props 接进去。

import { useCallback, useMemo, useRef, useState } from "react";
import type { Part } from "../../client";
import type { Attachment, MentionItem, UIEntry } from "../../ui";
import { useCommandContext, useRegistry, useShell, whenState } from "../context";

/** artifact.put 单次上限（appapi.MaxArtifactPut = 2 MiB）；更大的文件要走 HTTP 上传，首版直接提示。 */
export const MAX_ATTACHMENT = 2 << 20;

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

/** 附件：选中 / 粘贴 / 拖入即上传，发送时变成 media 部件。预览用 data URL（CSP 的 img-src 只放行 self 与 data:）。 */
export function useAttachments(session: string) {
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
        if (f.size > MAX_ATTACHMENT) {
          setItems((xs) => [...xs, { ...base, state: "failed", error: `超过 ${MAX_ATTACHMENT >> 20} MiB 上限` }]);
          continue;
        }
        setItems((xs) => [...xs, base]);
        void (async () => {
          try {
            const url = await readDataURL(f);
            if (mime.startsWith("image/")) patch(id, (a) => ({ ...a, preview: url }));
            const r = await sh.client.call("artifact.put", { session, mime, base64: url.slice(url.indexOf(",") + 1) });
            patch(id, (a) => ({ ...a, state: "ready", part: { kind: "media", media: { ref: r.ref, mime, size: f.size } } }));
          } catch (e) {
            patch(id, (a) => ({ ...a, state: "failed", error: e instanceof Error ? e.message : String(e) }));
          }
        })();
      }
    },
    [sh.client, session],
  );

  const remove = (id: string) => setItems((xs) => xs.filter((a) => a.id !== id));
  const parts = items.filter((a) => a.state === "ready" && a.part).map((a) => a.part!);
  const clear = () => setItems([]);
  const view: Attachment[] = items.map(({ part: _p, ...a }) => a);
  return { items: view, parts, add, remove, clear };
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

/** "@" 候选：合并各 MentionProvider（when 不满足的不出现，停用的给原因）。 */
export function useMentions(): ((q: string) => MentionItem[]) | undefined {
  const sh = useShell();
  const providers = useRegistry(sh.reg.mentions);
  const cx0 = useCommandContext();
  return useMemo(() => {
    if (!providers.length) return undefined;
    return (q: string) => {
      const out: MentionItem[] = [];
      for (const p of providers) {
        const w = whenState(p.when?.(cx0));
        if (!w.show) continue;
        let xs: MentionItem[] = [];
        try {
          xs = p.suggest(q, cx0);
        } catch (e) {
          if (p.pack) sh.packs.fail(p.pack, e instanceof Error ? e.message : String(e));
          continue;
        }
        for (const x of xs) out.push({ ...x, group: x.group || p.group, disabled: x.disabled || w.disabled, reason: x.reason ?? w.reason });
      }
      return out;
    };
  }, [providers, cx0.session, sh.packs]); // eslint-disable-line react-hooks/exhaustive-deps
}
