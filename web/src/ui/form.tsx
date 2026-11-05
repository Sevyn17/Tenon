// 由 JSON Schema 生成的表单（设置页的插件配置 L1、交互里的 form 种类共用）。
// 只支持常用子集：object 的 properties、string（含 enum）、number / integer、boolean、string[]；
// 其余类型显示成 JSON 文本框。readOnly 时只展示值，并可附一句说明（如“需编辑 profile.toml 后重启”）。

import { useState, type ReactNode } from "react";
import { Input, Segmented, Select, Switch, Textarea } from "./controls";
import { SettingsRow } from "./settings";

export interface JSONSchema {
  type?: string | string[];
  title?: string;
  description?: string;
  default?: unknown;
  enum?: unknown[];
  properties?: Record<string, JSONSchema>;
  required?: string[];
  items?: JSONSchema;
  minimum?: number;
  maximum?: number;
  "x-widget"?: string; // textarea / segmented / password
  "x-group"?: string;
}

export type FormValue = Record<string, unknown>;

function typeOf(s: JSONSchema): string {
  if (Array.isArray(s.type)) return s.type.find((t) => t !== "null") ?? "string";
  return s.type ?? (s.enum ? "string" : s.properties ? "object" : "string");
}

export function SchemaField(p: {
  name: string;
  schema: JSONSchema;
  value: unknown;
  onChange: (v: unknown) => void;
  readOnly?: boolean;
}): ReactNode {
  const { schema: sc, value, onChange, readOnly } = p;
  const v = value ?? sc.default;
  const t = typeOf(sc);
  if (t === "boolean") return <Switch checked={!!v} onChange={onChange} disabled={readOnly} />;
  if (sc.enum && sc.enum.length > 0) {
    const opts = sc.enum.map((x) => String(x));
    if (sc["x-widget"] === "segmented" || opts.length <= 4) {
      return (
        <Segmented
          value={String(v ?? "")}
          options={opts.map((o) => ({ value: o, label: o }))}
          onChange={(x) => !readOnly && onChange(x)}
        />
      );
    }
    return (
      <Select value={String(v ?? "")} disabled={readOnly} onChange={(e) => onChange(e.target.value)}>
        {opts.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </Select>
    );
  }
  if (t === "number" || t === "integer") {
    return (
      <Input
        type="number"
        readOnly={readOnly}
        min={sc.minimum}
        max={sc.maximum}
        step={t === "integer" ? 1 : "any"}
        value={v === undefined || v === null ? "" : String(v)}
        onChange={(e) => onChange(e.target.value === "" ? undefined : Number(e.target.value))}
        style={{ width: 120 }}
      />
    );
  }
  if (t === "array" && typeOf(sc.items ?? {}) === "string") {
    const arr = Array.isArray(v) ? (v as string[]) : [];
    return (
      <Textarea
        readOnly={readOnly}
        rows={Math.min(Math.max(arr.length, 2), 8)}
        value={arr.join("\n")}
        placeholder="每行一项"
        onChange={(e) => onChange(e.target.value.split("\n").filter((x) => x.trim() !== ""))}
      />
    );
  }
  if (t === "object" || t === "array") {
    return <JSONField value={v} readOnly={readOnly} onChange={onChange} />;
  }
  if (sc["x-widget"] === "textarea") {
    return <Textarea readOnly={readOnly} rows={4} value={String(v ?? "")} onChange={(e) => onChange(e.target.value)} />;
  }
  return (
    <Input
      type={sc["x-widget"] === "password" ? "password" : "text"}
      readOnly={readOnly}
      value={String(v ?? "")}
      onChange={(e) => onChange(e.target.value)}
      style={{ width: 240 }}
    />
  );
}

function JSONField({ value, readOnly, onChange }: { value: unknown; readOnly?: boolean; onChange: (v: unknown) => void }) {
  const [text, setText] = useState(() => JSON.stringify(value ?? null, null, 2));
  const [bad, setBad] = useState(false);
  return (
    <Textarea
      readOnly={readOnly}
      rows={5}
      value={text}
      aria-invalid={bad}
      style={{ fontFamily: "var(--font-mono)", width: 320 }}
      onChange={(e) => {
        setText(e.target.value);
        try {
          onChange(JSON.parse(e.target.value));
          setBad(false);
        } catch {
          setBad(true);
        }
      }}
    />
  );
}

/** object schema → 一组设置行。 */
export function SchemaForm(p: {
  schema: JSONSchema;
  value: FormValue;
  onChange: (v: FormValue) => void;
  readOnly?: boolean;
}) {
  const props = p.schema.properties ?? {};
  return (
    <>
      {Object.entries(props).map(([k, sc]) => (
        <SettingsRow
          key={k}
          id={k}
          title={sc.title ?? k}
          description={sc.description}
          required={p.schema.required?.includes(k)}
          control={
            <SchemaField
              name={k}
              schema={sc}
              value={p.value[k]}
              readOnly={p.readOnly}
              onChange={(v) => p.onChange({ ...p.value, [k]: v })}
            />
          }
        />
      ))}
    </>
  );
}
