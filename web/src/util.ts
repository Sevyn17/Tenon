// 公共小工具。

import { ApiError, type Content, type Entry } from "./protocol";

/** 内容块里的文本拼起来；非文本块用占位符。 */
export function textOf(c: Content | undefined): string {
  if (!c) return "";
  return c
    .map((p) => (p.kind === "text" ? (p.text ?? "") : `[${p.kind}${p.media?.MIME ? " " + p.media.MIME : ""}]`))
    .join("");
}

/** 预览：超长只取前 n 个字符（大结果只显示预览）。 */
export function preview(s: string, n = 2000): string {
  return s.length > n ? s.slice(0, n) + `\n…（共 ${s.length} 字符，只显示前 ${n}）` : s;
}

/** 参数 JSON 美化；不是 JSON 原样返回。 */
export function pretty(v: unknown): string {
  try {
    const o = typeof v === "string" ? JSON.parse(v) : v;
    return JSON.stringify(o, null, 2);
  } catch {
    return String(v);
  }
}

export function errText(e: unknown): string {
  if (e instanceof ApiError) return `${e.code}: ${e.message}`;
  return e instanceof Error ? e.message : String(e);
}

export function errCode(e: unknown): string {
  return e instanceof ApiError ? e.code : "";
}

/** 新会话 id：时间戳 + 随机后缀（只含字母数字与连字符）。 */
export function newSessionID(): string {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  const r = Math.random().toString(36).slice(2, 6);
  return `s-${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}-${r}`;
}

export function fmtTime(ms?: number): string {
  if (!ms) return "";
  const d = new Date(ms > 1e14 ? ms / 1e6 : ms); // 兼容纳秒 / 毫秒
  return d.toLocaleString();
}

export function entryText(e: Entry): string {
  const it = e.item;
  switch (it.kind) {
    case "user":
      return textOf(it.user?.Content);
    case "assistant":
      return textOf(it.assistant?.Content);
    case "tool_result":
      return textOf(it.tool_result?.Output);
    case "compaction":
      return textOf(it.compaction?.Summary);
    default:
      return "";
  }
}
