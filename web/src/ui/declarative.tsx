// 声明式 UI（前端方案第十一节）：后端插件不写前端代码，用一段 JSON 描述界面，这里渲染。
// 节点种类小而稳定；按钮只能触发宿主给的有限几种动作（调协议命令 / 打开面板 / 填入输入框 / 回答交互），
// 不能执行任意代码。未知节点渲染成"不支持的内容"占位，不会让整块挂掉。

import { useState, type ReactNode } from "react";
import { Badge, Button, type Tone } from "./controls";
import { CodeBlock, DiffView, KeyValue, Progress } from "./blocks";
import { Markdown } from "./markdown";
import { SchemaForm, type FormValue, type JSONSchema } from "./form";
import s from "./blocks.module.css";

/** 按钮能触发的动作。宿主决定怎么执行（shell 里接到协议客户端、面板、输入框）。 */
export type DeclAction =
  | { kind: "call"; cmd: string; args?: unknown }
  | { kind: "openPanel"; panel: string; params?: unknown }
  | { kind: "insert"; text: string }
  | { kind: "respond"; interaction: string; response: unknown };

export type DeclNode =
  | { type: "stack"; gap?: number; children: DeclNode[] }
  | { type: "row"; gap?: number; align?: "start" | "center" | "end"; children: DeclNode[] }
  | { type: "text"; text: string; tone?: "default" | "muted" | "danger" | "success"; size?: "sm" | "md" | "lg"; bold?: boolean }
  | { type: "markdown"; text: string }
  | { type: "badge"; text: string; tone?: Tone }
  | { type: "list"; items: DeclNode[]; ordered?: boolean }
  | { type: "table"; columns: string[]; rows: (string | number)[][] }
  | { type: "keyValue"; rows: [string, string][] }
  | { type: "progress"; value: number; label?: string }
  | { type: "code"; code: string; lang?: string }
  | { type: "diff"; diff: string }
  | { type: "button"; label: string; action: DeclAction; variant?: "default" | "primary" | "danger" }
  | { type: "link"; label: string; action: DeclAction }
  | { type: "form"; schema: JSONSchema; value?: Record<string, unknown>; submit: string; action: DeclAction }
  | { type: "divider" };

export interface DeclarativeProps {
  node: DeclNode | DeclNode[];
  /** 执行按钮动作；没给时按钮显示为不可用（不出现点了没反应的按钮）。 */
  onAction?: (a: DeclAction, values?: Record<string, unknown>) => void | Promise<void>;
}

export function DeclarativeView({ node, onAction }: DeclarativeProps) {
  const nodes = Array.isArray(node) ? node : [node];
  return <>{nodes.map((n, i) => render(n, `d${i}`, onAction))}</>;
}

function render(n: DeclNode, key: string, act?: DeclarativeProps["onAction"]): ReactNode {
  if (!n || typeof n !== "object" || !("type" in n)) return <Unsupported key={key} what="?" />;
  switch (n.type) {
    case "stack":
      return (
        <div key={key} className={s.declStack} style={{ gap: (n.gap ?? 2) * 4 }}>
          {(n.children ?? []).map((c, i) => render(c, `${key}.${i}`, act))}
        </div>
      );
    case "row":
      return (
        <div key={key} className={s.declRow} style={{ gap: (n.gap ?? 2) * 4, alignItems: n.align ?? "center" }}>
          {(n.children ?? []).map((c, i) => render(c, `${key}.${i}`, act))}
        </div>
      );
    case "text":
      return (
        <span key={key} className={s.declText} data-tone={n.tone ?? "default"} data-size={n.size ?? "md"} data-bold={n.bold || undefined}>
          {n.text}
        </span>
      );
    case "markdown":
      return <Markdown key={key} text={n.text} />;
    case "badge":
      return (
        <Badge key={key} tone={n.tone}>
          {n.text}
        </Badge>
      );
    case "list": {
      const Tag = n.ordered ? "ol" : "ul";
      return (
        <Tag key={key} className={s.declList}>
          {n.items.map((c, i) => (
            <li key={i}>{render(c, `${key}.${i}`, act)}</li>
          ))}
        </Tag>
      );
    }
    case "table":
      return (
        <table key={key} className={s.declTable}>
          <thead>
            <tr>
              {n.columns.map((c) => (
                <th key={c}>{c}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {n.rows.map((r, i) => (
              <tr key={i}>
                {r.map((c, j) => (
                  <td key={j}>{c}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      );
    case "keyValue":
      return <KeyValue key={key} rows={n.rows} />;
    case "progress":
      return (
        <div key={key} className={s.declStack}>
          {n.label && <span className={s.declText} data-tone="muted" data-size="sm">{n.label}</span>}
          <Progress value={n.value} />
        </div>
      );
    case "code":
      return <CodeBlock key={key} code={n.code} lang={n.lang} />;
    case "diff":
      return <DiffView key={key} diff={n.diff} />;
    case "button":
      return (
        <Button key={key} size="sm" variant={n.variant ?? "default"} disabled={!act} onClick={() => void act?.(n.action)}>
          {n.label}
        </Button>
      );
    case "link":
      return (
        <button key={key} type="button" className={s.declLink} disabled={!act} onClick={() => void act?.(n.action)}>
          {n.label}
        </button>
      );
    case "form":
      return (
        <DeclForm key={key} node={n} act={act} />
      );
    case "divider":
      return <hr key={key} className={s.declDivider} />;
    default:
      return <Unsupported key={key} what={(n as { type: string }).type} />;
  }
}

function Unsupported({ what }: { what: string }) {
  return <span className={s.declText} data-tone="muted" data-size="sm">（不支持的内容：{what}）</span>;
}

/** 表单节点：本地保存填写的值，提交时把值随动作交给宿主。 */
function DeclForm({ node, act }: { node: Extract<DeclNode, { type: "form" }>; act?: DeclarativeProps["onAction"] }) {
  const [v, setV] = useState<FormValue>(node.value ?? {});
  const [busy, setBusy] = useState(false);
  return (
    <div className={s.declStack} style={{ gap: 8 }}>
      <SchemaForm schema={node.schema} value={v} onChange={setV} readOnly={!act || busy} />
      <div className={s.declRow}>
        <Button
          size="sm"
          variant="primary"
          disabled={!act || busy}
          onClick={async () => {
            setBusy(true);
            try {
              await act?.(node.action, v);
            } finally {
              setBusy(false);
            }
          }}
        >
          {node.submit}
        </Button>
      </div>
    </div>
  );
}
