// 极简 Markdown 渲染：只生成 React 元素，不用 innerHTML，原始 HTML 一律按文本显示（安全要求）。
// 支持：代码块 ```、标题 #、列表 - / 1.、引用 >、行内 `代码`、**粗体**、*斜体*、[文字](链接)。
// 链接不在窗口内跳转（桌面端没有外部浏览器桥），只显示文字并在悬停提示地址。

import { type ReactNode } from "react";

function inline(text: string, key: string): ReactNode[] {
  const out: ReactNode[] = [];
  // 依次匹配：行内代码、粗体、斜体、链接
  const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*\s][^*]*\*)|(\[[^\]]+\]\([^)\s]+\))/g;
  let last = 0;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const s = m[0];
    const k = `${key}-${i++}`;
    if (m[1]) out.push(<code key={k}>{s.slice(1, -1)}</code>);
    else if (m[2]) out.push(<strong key={k}>{s.slice(2, -2)}</strong>);
    else if (m[3]) out.push(<em key={k}>{s.slice(1, -1)}</em>);
    else {
      const mm = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(s);
      out.push(
        <span key={k} className="md-link" title={mm?.[2] ?? ""}>
          {mm?.[1] ?? s}
        </span>,
      );
    }
    last = m.index + s.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

export function Markdown({ text }: { text: string }) {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  const blocks: ReactNode[] = [];
  let i = 0;
  let n = 0;
  while (i < lines.length) {
    const line = lines[i];
    const key = `b${n++}`;
    if (line.startsWith("```")) {
      const lang = line.slice(3).trim();
      const body: string[] = [];
      i++;
      while (i < lines.length && !lines[i].startsWith("```")) body.push(lines[i++]);
      i++;
      blocks.push(
        <pre key={key} className="md-code" data-lang={lang}>
          <code>{body.join("\n")}</code>
        </pre>,
      );
      continue;
    }
    const h = /^(#{1,6})\s+(.*)$/.exec(line);
    if (h) {
      const lvl = Math.min(h[1].length + 2, 6);
      const Tag = `h${lvl}` as "h3";
      blocks.push(<Tag key={key}>{inline(h[2], key)}</Tag>);
      i++;
      continue;
    }
    if (/^\s*([-*+]|\d+\.)\s+/.test(line)) {
      const ordered = /^\s*\d+\./.test(line);
      const items: ReactNode[] = [];
      while (i < lines.length && /^\s*([-*+]|\d+\.)\s+/.test(lines[i])) {
        const t = lines[i].replace(/^\s*([-*+]|\d+\.)\s+/, "");
        items.push(<li key={`${key}-${i}`}>{inline(t, `${key}-${i}`)}</li>);
        i++;
      }
      blocks.push(ordered ? <ol key={key}>{items}</ol> : <ul key={key}>{items}</ul>);
      continue;
    }
    if (line.startsWith(">")) {
      const body: string[] = [];
      while (i < lines.length && lines[i].startsWith(">")) body.push(lines[i++].replace(/^>\s?/, ""));
      blocks.push(<blockquote key={key}>{inline(body.join(" "), key)}</blockquote>);
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
      !/^\s*([-*+]|\d+\.)\s+/.test(lines[i]) &&
      !lines[i].startsWith(">")
    )
      para.push(lines[i++]);
    blocks.push(<p key={key}>{inline(para.join("\n"), key)}</p>);
  }
  return <div className="md">{blocks}</div>;
}
