// Markdown 渲染：只生成 React 元素，不用 innerHTML，原始 HTML 一律按文本显示（安全要求）。
// 支持：代码块（流式时未闭合的代码块照样按代码块显示，避免整段跳动）、标题、有序 / 无序列表、引用、表格、分隔线、
// 行内代码、粗体、斜体、链接。代码块可按语言注册扩展渲染（如 mermaid、diff），见 MarkdownExtension。

import { createContext, useContext, useMemo, type ReactNode } from "react";
import { CodeBlock, DiffView } from "./blocks";
import s from "./markdown.module.css";

/** 代码块扩展：按语言接管渲染（返回 null 交回缺省代码块）。 */
export interface MarkdownExtension {
  id: string;
  langs: string[];
  render(code: string, lang: string, streaming: boolean): ReactNode | null;
}

const ExtCtx = createContext<MarkdownExtension[]>([]);

/** 提供代码块扩展（shell 把注册表接到这里）。 */
export function MarkdownExtensions({ value, children }: { value: MarkdownExtension[]; children: ReactNode }) {
  return <ExtCtx.Provider value={value}>{children}</ExtCtx.Provider>;
}

/** 链接点击：缺省不跳转（桌面端没有外部浏览器桥），嵌入方可替换。 */
const LinkCtx = createContext<((href: string) => void) | null>(null);
export const MarkdownLinkHandler = LinkCtx.Provider;

function Inline({ text }: { text: string }) {
  const onLink = useContext(LinkCtx);
  const out: ReactNode[] = [];
  const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*\s][^*]*\*)|(\[[^\]]+\]\([^)\s]+\))/g;
  let last = 0;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const t = m[0];
    const k = i++;
    if (m[1]) out.push(<code key={k} className={s.ic}>{t.slice(1, -1)}</code>);
    else if (m[2]) out.push(<strong key={k}>{t.slice(2, -2)}</strong>);
    else if (m[3]) out.push(<em key={k}>{t.slice(1, -1)}</em>);
    else {
      const mm = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(t);
      const href = mm?.[2] ?? "";
      out.push(
        <a
          key={k}
          className={s.link}
          title={href}
          href={onLink ? href : undefined}
          onClick={(e) => {
            if (!onLink) return;
            e.preventDefault();
            onLink(href);
          }}
        >
          {mm?.[1] ?? t}
        </a>,
      );
    }
    last = m.index + t.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return <>{out}</>;
}

function Code({ code, lang, streaming }: { code: string; lang: string; streaming: boolean }) {
  const exts = useContext(ExtCtx);
  for (const x of exts) {
    if (x.langs.includes(lang)) {
      const r = x.render(code, lang, streaming);
      if (r !== null) return <>{r}</>;
    }
  }
  if (lang === "diff" || lang === "patch") return <DiffView diff={code} />;
  return <CodeBlock code={code} lang={lang} />;
}

const LIST = /^\s*([-*+]|\d+\.)\s+/;
const TABLE_SEP = /^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$/;

function cells(line: string): string[] {
  return line.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map((c) => c.trim());
}

export function Markdown({ text, streaming = false, className }: { text: string; streaming?: boolean; className?: string }) {
  const blocks = useMemo(() => parse(text, streaming), [text, streaming]);
  return <div className={className ? `${s.md} ${className}` : s.md}>{blocks}</div>;
}

function parse(text: string, streaming: boolean): ReactNode[] {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  const out: ReactNode[] = [];
  let i = 0;
  let n = 0;
  while (i < lines.length) {
    const line = lines[i];
    const key = n++;
    if (line.startsWith("```")) {
      const lang = line.slice(3).trim().toLowerCase();
      const body: string[] = [];
      i++;
      while (i < lines.length && !lines[i].startsWith("```")) body.push(lines[i++]);
      const closed = i < lines.length;
      i++;
      out.push(<Code key={key} code={body.join("\n")} lang={lang} streaming={streaming && !closed} />);
      continue;
    }
    const h = /^(#{1,6})\s+(.*)$/.exec(line);
    if (h) {
      const lvl = Math.min(h[1].length, 4);
      const Tag = `h${lvl}` as "h1";
      out.push(
        <Tag key={key} className={s[`h${lvl}`]}>
          <Inline text={h[2]} />
        </Tag>,
      );
      i++;
      continue;
    }
    if (/^\s*(-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
      out.push(<hr key={key} className={s.hr} />);
      i++;
      continue;
    }
    if (line.includes("|") && i + 1 < lines.length && TABLE_SEP.test(lines[i + 1])) {
      const head = cells(line);
      i += 2;
      const rows: string[][] = [];
      while (i < lines.length && lines[i].includes("|") && lines[i].trim() !== "") rows.push(cells(lines[i++]));
      out.push(
        <div key={key} className={s.tableWrap}>
          <table className={s.table}>
            <thead>
              <tr>
                {head.map((c, j) => (
                  <th key={j}>
                    <Inline text={c} />
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((r, ri) => (
                <tr key={ri}>
                  {r.map((c, j) => (
                    <td key={j}>
                      <Inline text={c} />
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>,
      );
      continue;
    }
    if (LIST.test(line)) {
      const ordered = /^\s*\d+\./.test(line);
      const items: ReactNode[] = [];
      while (i < lines.length && LIST.test(lines[i])) {
        const t = lines[i].replace(LIST, "");
        const task = /^\[( |x|X)\]\s+/.exec(t);
        items.push(
          <li key={i} className={task ? s.task : undefined}>
            {task && <input type="checkbox" readOnly checked={task[1] !== " "} />}
            <Inline text={task ? t.slice(task[0].length) : t} />
          </li>,
        );
        i++;
      }
      out.push(
        ordered ? (
          <ol key={key} className={s.list}>
            {items}
          </ol>
        ) : (
          <ul key={key} className={s.list}>
            {items}
          </ul>
        ),
      );
      continue;
    }
    if (line.startsWith(">")) {
      const body: string[] = [];
      while (i < lines.length && lines[i].startsWith(">")) body.push(lines[i++].replace(/^>\s?/, ""));
      out.push(
        <blockquote key={key} className={s.quote}>
          <Inline text={body.join(" ")} />
        </blockquote>,
      );
      continue;
    }
    if (line.trim() === "") {
      i++;
      continue;
    }
    const para: string[] = [];
    while (
      i < lines.length &&
      lines[i].trim() !== "" &&
      !lines[i].startsWith("```") &&
      !/^(#{1,6})\s/.test(lines[i]) &&
      !LIST.test(lines[i]) &&
      !lines[i].startsWith(">")
    )
      para.push(lines[i++]);
    out.push(
      <p key={key} className={s.p}>
        <Inline text={para.join("\n")} />
      </p>,
    );
  }
  return out;
}
