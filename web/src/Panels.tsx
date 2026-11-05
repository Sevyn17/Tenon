// 侧栏与面板：会话列表、权限档位、插件、统计。

import { useCallback, useEffect, useState } from "react";
import type { ConfigField as Field, Mode as ModeState, ModeInfo, ModeItem, PluginInfo, SessionInfo, Stats } from "./wire.gen";
import { call } from "./transport";
import { errCode, errText, fmtTime } from "./util";

// ---- 会话列表（分页） ----

export function SessionList(props: { current: string; onPick: (id: string) => void; onNew: () => void; refresh: number }) {
  const [items, setItems] = useState<SessionInfo[]>([]);
  const [next, setNext] = useState(0);
  const [all, setAll] = useState(false);
  const [err, setErr] = useState("");

  const load = useCallback(
    async (cursor: number) => {
      setErr("");
      try {
        const r = await call("sessions.list", { cursor, limit: 50, all });
        const got = r.items ?? [];
        setItems((xs) => (cursor === 0 ? got : [...xs, ...got]));
        setNext(r.next);
      } catch (e) {
        setErr(errText(e));
      }
    },
    [all],
  );

  useEffect(() => {
    void load(0);
  }, [load, props.refresh]);

  return (
    <div className="sessions">
      <div className="row">
        <button onClick={props.onNew}>+ 新会话</button>
        <label className="muted">
          <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> 含子会话
        </label>
      </div>
      {err && <div className="error">{err}</div>}
      <ul>
        {items.map((s) => (
          <li key={s.id} className={s.id === props.current ? "on" : ""} onClick={() => props.onPick(s.id)} title={s.id}>
            <div className="title">{s.title || s.id}</div>
            <div className="muted small">
              {s.run_state === "running" ? "运行中 · " : ""}
              {fmtTime(s.updated_at)}
            </div>
            {s.preview && <div className="muted small ellipsis">{s.preview}</div>}
          </li>
        ))}
      </ul>
      {next > 0 && <button onClick={() => void load(next)}>加载更多</button>}
    </div>
  );
}

// ---- 权限档位 ----

export function ModesPanel({ session }: { session: string }) {
  const [list, setList] = useState<ModeInfo[]>([]);
  const [state, setState] = useState<ModeState | null>(null);
  const [items, setItems] = useState<ModeItem[]>([]);
  const [showing, setShowing] = useState("");
  const [confirm, setConfirm] = useState<ModeInfo | null>(null);
  const [err, setErr] = useState("");

  const refresh = useCallback(async () => {
    setErr("");
    try {
      setList((await call("mode.list", {})).modes ?? []);
      if (session) setState((await call("mode.get", { session })).state);
    } catch (e) {
      setErr(errText(e));
    }
  }, [session]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const show = async (mode: string) => {
    try {
      setItems((await call("mode.show", { session, mode })).items ?? []);
      setShowing(mode);
    } catch (e) {
      setErr(errText(e));
    }
  };

  const set = async (m: ModeInfo, ok = false) => {
    setErr("");
    try {
      setState((await call("mode.set", { session, mode: m.id, confirm: ok })).state);
      setConfirm(null);
    } catch (e) {
      if (errCode(e) === "need_confirm") {
        setConfirm(m);
        void show(m.id);
      } else setErr(errText(e));
    }
  };

  if (!session) return <div className="muted">先选择会话</div>;
  return (
    <div className="panel">
      <h3>权限档位</h3>
      {state && (
        <div>
          当前：<b>{state.label || state.mode}</b> <span className="muted">（沙箱 {state.sandbox || "无"}）</span>
        </div>
      )}
      <ul className="modes">
        {list.map((m) => (
          <li key={m.id} className={state?.mode === m.id ? "on" : ""}>
            <b>{m.label || m.id}</b> <span className="muted">{m.id}</span>
            {m.project && <span className="tag">项目</span>}
            {m.builtin && <span className="tag">内置</span>}
            {m.widened && m.widened.length > 0 && <div className="warn small">放宽：{m.widened.join("、")}</div>}
            <div className="row">
              <button disabled={state?.mode === m.id} onClick={() => void set(m)}>
                切换
              </button>
              <button onClick={() => void show(m.id)}>查看</button>
            </div>
          </li>
        ))}
      </ul>
      {confirm && (
        <div className="confirm-box">
          <div className="warn">
            项目档位「{confirm.label || confirm.id}」放宽了权限：{(confirm.widened ?? []).join("、")}。确认切换？
          </div>
          <div className="row">
            <button className="danger" onClick={() => void set(confirm, true)}>
              确认切换
            </button>
            <button onClick={() => setConfirm(null)}>取消</button>
          </div>
        </div>
      )}
      {showing && (
        <table className="items">
          <caption>档位 {showing} 的取值</caption>
          <thead>
            <tr>
              <th>权限项</th>
              <th>取值</th>
              <th>来源</th>
            </tr>
          </thead>
          <tbody>
            {items.map((it) => (
              <tr key={it.item}>
                <td title={it.item}>{it.title || it.item}</td>
                <td>
                  {it.value}
                  {!it.bounded && <span className="muted small">（非边界）</span>}
                </td>
                <td className="muted">{it.source}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {err && <div className="error">{err}</div>}
    </div>
  );
}

// ---- 插件（配置表单由 schema 生成） ----

function FieldInput(props: { f: Field; value: string; readOnly: boolean; onChange: (v: string) => void }) {
  const { f } = props;
  if (f.type === "bool")
    return (
      <input
        type="checkbox"
        disabled={props.readOnly}
        checked={props.value === "true"}
        onChange={(e) => props.onChange(String(e.target.checked))}
      />
    );
  const num = /^(u?int\d*|float\d*)$/.test(f.type);
  return (
    <input
      type={num ? "number" : "text"}
      readOnly={props.readOnly}
      value={props.value}
      placeholder={f.default ?? ""}
      onChange={(e) => props.onChange(e.target.value)}
    />
  );
}

function PluginForm({ p }: { p: PluginInfo }) {
  const [vals, setVals] = useState<Record<string, string>>({});
  const [on, setOn] = useState(p.on);
  const [readOnly, setReadOnly] = useState(false);
  const [msg, setMsg] = useState("");
  const schema = p.schema ?? [];
  const groups = new Map<string, Field[]>();
  for (const f of schema) {
    const g = f.group ?? "";
    groups.set(g, [...(groups.get(g) ?? []), f]);
  }
  const save = async () => {
    setMsg("");
    const config: Record<string, unknown> = {};
    for (const f of schema) {
      const v = vals[f.key];
      if (v === undefined) continue;
      config[f.key] = f.type === "bool" ? v === "true" : /^(u?int\d*|float\d*)$/.test(f.type) ? Number(v) : v;
    }
    try {
      await call("plugins.set", { name: p.name, enabled: on, config, scope: "user" });
      setMsg("已保存");
    } catch (e) {
      if (errCode(e) === "unsupported") setReadOnly(true);
      setMsg(errText(e));
    }
  };
  return (
    <div className="plugin-form">
      <label>
        <input type="checkbox" checked={on} disabled={readOnly} onChange={(e) => setOn(e.target.checked)} /> 启用
        <span className="muted small">（缺省 {p.default ? "开" : "关"}{p.source ? ` · 来源 ${p.source}` : ""}）</span>
      </label>
      {[...groups.entries()].map(([g, fs]) => (
        <fieldset key={g}>
          {g && <legend>{g}</legend>}
          {fs.map((f) => (
            <div key={f.key} className="field">
              <label title={f.key}>
                {f.title || f.key}
              </label>
              <FieldInput
                f={f}
                readOnly={readOnly}
                value={vals[f.key] ?? f.default ?? ""}
                onChange={(v) => setVals((x) => ({ ...x, [f.key]: v }))}
              />
              {f.description && <div className="muted small">{f.description}</div>}
            </div>
          ))}
        </fieldset>
      ))}
      {schema.length === 0 && <div className="muted small">无可配置项</div>}
      {readOnly ? (
        <div className="muted small">只读：当前版本不支持在界面修改，请编辑 profile.toml 后重启。</div>
      ) : (
        <button onClick={() => void save()}>保存</button>
      )}
      {msg && <div className={readOnly ? "muted small" : "small"}>{msg}</div>}
    </div>
  );
}

export function PluginsPanel() {
  const [list, setList] = useState<PluginInfo[]>([]);
  const [open, setOpen] = useState("");
  const [err, setErr] = useState("");
  useEffect(() => {
    call("plugins.list", {})
      .then((r) => setList(r.plugins ?? []))
      .catch((e) => setErr(errText(e)));
  }, []);
  return (
    <div className="panel">
      <h3>插件</h3>
      {err && <div className="error">{err}</div>}
      <ul className="plugins">
        {list.map((p) => (
          <li key={p.name}>
            <div className="tool-head" onClick={() => setOpen(open === p.name ? "" : p.name)}>
              <span>{open === p.name ? "▾" : "▸"}</span> <b>{p.name}</b>{" "}
              <span className={p.on ? "ok" : "muted"}>{p.on ? "已启用" : "未启用"}</span>
            </div>
            {open === p.name && <PluginForm p={p} />}
          </li>
        ))}
      </ul>
    </div>
  );
}

// ---- 统计 ----

export function StatsPanel({ session }: { session: string }) {
  const [s, setS] = useState<Stats | null>(null);
  const [err, setErr] = useState("");
  const load = useCallback(() => {
    if (!session) return;
    setErr("");
    call("session.stats", { session })
      .then(setS)
      .catch((e) => setErr(errText(e)));
  }, [session]);
  useEffect(load, [load]);
  if (!session) return <div className="muted">先选择会话</div>;
  const rows: [string, string | number][] = s
    ? [
        ["请求次数", s.attempts],
        ["有用量上报", s.reported],
        ["缺用量", s.missing],
        ["出错", s.errors],
        ["输入 tokens", s.input],
        ["输出 tokens", s.output],
        ["缓存读", s.cache_read],
        ["缓存写", s.cache_write],
        ["缓存命中率", s.hit_rate < 0 ? "无数据" : (s.hit_rate * 100).toFixed(1) + "%"],
        ["工具调用", s.tool_calls],
        ["压缩次数", s.compacts],
      ]
    : [];
  return (
    <div className="panel">
      <h3>
        统计 <button onClick={load}>刷新</button>
      </h3>
      {err && <div className="error">{err}</div>}
      <table className="items">
        <tbody>
          {rows.map(([k, v]) => (
            <tr key={k}>
              <td>{k}</td>
              <td>{v}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {s?.composition != null && (
        <details>
          <summary>上下文构成</summary>
          <pre>{JSON.stringify(s.composition, null, 2)}</pre>
        </details>
      )}
      {s?.last_request != null && (
        <details>
          <summary>最近一次请求</summary>
          <pre>{JSON.stringify(s.last_request, null, 2)}</pre>
        </details>
      )}
    </div>
  );
}
