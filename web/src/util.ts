// 公共小工具。

import { ApiError } from "./client/commands";
import type { Entry, Part } from "./client/wire.gen";

/** 内容片段里的文本拼起来；非文本片段用占位符。 */
export function textOf(c: Part[] | undefined): string {
  if (!c) return "";
  return c
    .map((p) => (p.kind === "text" ? (p.text ?? "") : `[${p.kind}${p.media?.mime ? " " + p.media.mime : ""}]`))
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

/** conflict 的 details.reason（exists / answered / stale / leased）。 */
export function errReason(e: unknown): string {
  if (!(e instanceof ApiError)) return "";
  const r = e.details?.["reason"];
  return typeof r === "string" ? r : "";
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
  return new Date(ms).toLocaleString();
}

export function entryText(e: Entry): string {
  const it = e.item;
  switch (it.kind) {
    case "user":
    case "assistant":
      return textOf(it.content);
    case "tool_result":
      return textOf(it.output);
    case "compaction":
      return textOf(it.summary);
    case "context":
      return it.text ?? "";
    default:
      return "";
  }
}
