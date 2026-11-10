// 输入框工具栏的内置贡献（core 包注册到 composer.toolbar.left / .right）：模型、推理强度、权限档位、上下文用量。
// 首页草稿（session 为 null，会话还没建）也能选模型、推理强度与档位：选择记在 sh.draft，建会话时应用。
// 嵌入方要换掉或关掉某一项：同 id 更小的 priority 替换，或 disable 掉 core 包里对应的插槽。

import { Brain, Cpu, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import type { Client, ModeInfo, ModelsListResult } from "../../client";
import { useSession, useStore } from "../../client/react";
import { Button, Menu, ProgressRing, type MenuGroupSpec } from "../../ui";
import { useShell } from "../context";
import { confirmProjectMode, setSessionMode, shortModeLabel } from "../modes";

// ---- 数据：整个页面共用一份，第一次用到时取 ----

const cache = new WeakMap<Client, { models?: Promise<ModelsListResult>; modes?: Promise<ModeInfo[]> }>();
function slot(client: Client) {
  let c = cache.get(client);
  if (!c) cache.set(client, (c = {}));
  return c;
}

export function loadModels(client: Client): Promise<ModelsListResult> {
  const c = slot(client);
  c.models ??= client.call("models.list", {}).catch((e) => {
    c.models = undefined;
    throw e;
  });
  return c.models;
}

function loadModes(client: Client): Promise<ModeInfo[]> {
  const c = slot(client);
  c.modes ??= client.call("mode.list", {}).then(
    (r) => r.modes,
    (e) => {
      c.modes = undefined;
      throw e;
    },
  );
  return c.modes;
}

function useLoaded<T>(can: boolean, load: () => Promise<T>): T | null {
  const [v, setV] = useState<T | null>(null);
  useEffect(() => {
    if (!can) return;
    let live = true;
    load().then((r) => live && setV(r), () => {});
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [can]);
  return v;
}

function useModels(): ModelsListResult | null {
  const sh = useShell();
  return useLoaded(sh.client.can("models"), () => loadModels(sh.client));
}

const fmtK = (n: number) => (n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n));
const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));

// ---- 模型 ----

/** 模型选择：会话态改 session.settings（回合中切换在回合结束后生效）；草稿态记进 sh.draft。 */
export function ModelPicker({ session }: { session: string | null }) {
  return session ? <SessionModelPicker session={session} /> : <DraftModelPicker />;
}

function SessionModelPicker({ session }: { session: string }) {
  const sh = useShell();
  const [st] = useSession(session);
  const list = useModels();
  if (!list?.models.length) return null;
  return (
    <ModelMenu
      list={list}
      current={st.model || list.default}
      onPick={async (id) => {
        try {
          const r = await sh.client.call("session.settings", { session, model: id });
          if (!r.committed) sh.api.toast(`回合结束后换成 ${id}`);
        } catch (e) {
          sh.api.toast(errText(e), "danger");
        }
      }}
    />
  );
}

function DraftModelPicker() {
  const sh = useShell();
  const draft = useStore(sh.draft);
  const list = useModels();
  if (!list?.models.length) return null;
  return <ModelMenu list={list} current={draft.model || list.default} onPick={(id) => sh.draft.update((d) => ({ ...d, model: id }))} />;
}

/** 别名一组、模型按提供方分组；凭证不可用的停用并给原因。 */
function ModelMenu({ list, current, onPick }: { list: ModelsListResult; current: string; onPick: (id: string) => void }) {
  const groups = (): MenuGroupSpec[] => {
    const byGroup = new Map<string, MenuGroupSpec>();
    for (const m of list.models) {
      const g = m.alias ? "别名" : m.provider;
      if (!byGroup.has(g)) byGroup.set(g, { id: g, label: g, items: [] });
      byGroup.get(g)!.items.push({
        id: m.id,
        label: m.alias ? m.id : m.name || m.id,
        description:
          [m.alias && m.name ? m.name : "", m.context_window ? `${Math.round(m.context_window / 1000)}k 上下文` : "", m.images ? "图片" : "", m.reasoning ? "推理" : ""]
            .filter(Boolean)
            .join(" · ") || undefined,
        checked: m.id === current,
        disabled: !m.available,
        reason: m.reason,
        run: () => onPick(m.id),
      });
    }
    return [...byGroup.values()];
  };
  const short = current.includes("/") ? current.slice(current.indexOf("/") + 1) : current;
  return (
    <Menu
      placement="top-start"
      groups={groups}
      trigger={({ ref, toggle }) => (
        <Button ref={ref} size="sm" variant="ghost" onClick={toggle} aria-label={`模型：${current}，点击切换`} title={current}>
          <Cpu size={12} /> {short}
        </Button>
      )}
    />
  );
}

// ---- 推理强度 ----

const EFFORT_LABEL: Record<string, string> = { low: "低", medium: "中", high: "高" };

/** 推理强度：当前模型在 models.list 里给了 efforts 才显示。会话态改 session.settings {reasoning}，草稿态记进 sh.draft。
 * 换到不支持推理的模型时设置保留，后端不下发（换回来还在）。 */
export function ReasoningPicker({ session }: { session: string | null }) {
  return session ? <SessionReasoningPicker session={session} /> : <DraftReasoningPicker />;
}

function SessionReasoningPicker({ session }: { session: string }) {
  const sh = useShell();
  const [st] = useSession(session);
  const list = useModels();
  const efforts = list?.models.find((m) => m.id === (st.model || list.default))?.efforts;
  if (!efforts?.length) return null;
  return (
    <ReasoningMenu
      efforts={efforts}
      current={st.reasoning}
      onPick={async (v) => {
        try {
          const r = await sh.client.call("session.settings", { session, reasoning: v });
          if (!r.committed) sh.api.toast(`回合结束后推理强度换成 ${EFFORT_LABEL[v] ?? "自动"}`);
        } catch (e) {
          sh.api.toast(errText(e), "danger");
        }
      }}
    />
  );
}

function DraftReasoningPicker() {
  const sh = useShell();
  const draft = useStore(sh.draft);
  const list = useModels();
  const efforts = list?.models.find((m) => m.id === (draft.model || list.default))?.efforts;
  if (!efforts?.length) return null;
  return <ReasoningMenu efforts={efforts} current={draft.reasoning ?? ""} onPick={(v) => sh.draft.update((d) => ({ ...d, reasoning: v || undefined }))} />;
}

function ReasoningMenu({ efforts, current, onPick }: { efforts: string[]; current: string; onPick: (v: string) => void }) {
  const groups = (): MenuGroupSpec[] => [
    {
      id: "reasoning",
      label: "推理强度",
      items: [
        { id: "auto", label: "自动", description: "用模型厂商的缺省", checked: !current, run: () => onPick("") },
        ...efforts.map((e) => ({ id: e, label: EFFORT_LABEL[e] ?? e, checked: e === current, run: () => onPick(e) })),
      ],
    },
  ];
  const label = current ? `推理 ${EFFORT_LABEL[current] ?? current}` : "推理";
  return (
    <Menu
      placement="top-start"
      groups={groups}
      trigger={({ ref, toggle }) => (
        <Button ref={ref} size="sm" variant="ghost" onClick={toggle} aria-label={`推理强度：${current ? EFFORT_LABEL[current] ?? current : "自动"}，点击切换`} title="推理强度">
          <Brain size={12} /> {label}
        </Button>
      )}
    />
  );
}

// ---- 权限档位 ----

/** 档位选择（后端有 modes 能力时）：会话态 mode.set（项目档位先确认）；草稿态记进 sh.draft，选项目档位时当场确认。 */
export function ModePicker({ session }: { session: string | null }) {
  const sh = useShell();
  const modes = useLoaded(sh.client.can("modes"), () => loadModes(sh.client));
  if (!modes?.length) return null;
  return session ? <SessionModePicker session={session} modes={modes} /> : <DraftModePicker modes={modes} />;
}

function SessionModePicker({ session, modes }: { session: string; modes: ModeInfo[] }) {
  const sh = useShell();
  const [st] = useSession(session);
  const cur = st.mode?.mode;
  const short = modes.find((m) => m.id === cur)?.short;
  return (
    <ModeMenu
      modes={modes}
      current={cur}
      label={short || (st.mode ? shortModeLabel(st.mode.label, st.mode.mode) : "档位")}
      title={st.mode?.label}
      onPick={async (m) => {
        try {
          if (await setSessionMode(sh.client, sh.api, session, m)) sh.api.toast(`档位：${m.label}`, "success");
        } catch (e) {
          sh.api.toast(errText(e), "danger");
        }
      }}
    />
  );
}

function DraftModePicker({ modes }: { modes: ModeInfo[] }) {
  const sh = useShell();
  const draft = useStore(sh.draft);
  const chosen = modes.find((m) => m.id === draft.mode);
  return (
    <ModeMenu
      modes={modes}
      current={draft.mode}
      label={chosen?.short || (chosen ? shortModeLabel(chosen.label, chosen.id) : "默认档位")}
      title={chosen ? chosen.label : "新会话用后端的缺省档位"}
      onPick={async (m) => {
        if (m.project && !(await confirmProjectMode(sh.api, m))) return;
        sh.draft.update((d) => ({ ...d, mode: m.id, modeConfirmed: m.project || undefined }));
      }}
    />
  );
}

function ModeMenu(p: { modes: ModeInfo[]; current?: string; label: string; title?: string; onPick: (m: ModeInfo) => void }) {
  const groups = (): MenuGroupSpec[] => [
    {
      id: "modes",
      label: "权限档位",
      items: p.modes.map((m) => ({
        id: m.id,
        label: m.short || m.label,
        description:
          [m.label !== (m.short || m.label) ? m.label : undefined, m.widened?.length ? `项目档位，放宽：${m.widened.join("、")}` : m.project ? "项目档位" : undefined]
            .filter(Boolean)
            .join(" · ") || undefined,
        checked: m.id === p.current,
        run: () => p.onPick(m),
      })),
    },
  ];
  return (
    <Menu
      placement="top-start"
      groups={groups}
      trigger={({ ref, toggle }) => (
        <Button ref={ref} size="sm" variant="ghost" onClick={toggle} aria-label={`权限档位：${p.title ?? p.label}，点击切换`} title={p.title}>
          <ShieldCheck size={12} /> {p.label}
        </Button>
      )}
    />
  );
}

// ---- 上下文用量 ----

/** 上下文占用环（06 第五节）：上一次请求的占用超过模型上下文窗口 80% 时，发送按钮旁出现小环
 * （models.list 的 context_window），95% 转红；点开"上下文与用量"面板。占用不高时不出现在工具栏，
 * 明细随时可在面板看。草稿态没有用量，不显示。 */
export function ContextMeter({ session }: { session: string | null }) {
  return session ? <SessionContextMeter session={session} /> : null;
}

function SessionContextMeter({ session }: { session: string }) {
  const sh = useShell();
  const [st] = useSession(session);
  const list = useModels();
  let used = 0;
  for (let i = st.entries.length - 1; i >= 0; i--) {
    const it = st.entries[i].item;
    if (it.kind === "assistant" && it.usage) {
      used = it.usage.input;
      break;
    }
  }
  if (!used) return null;
  const model = st.model || list?.default;
  const win = list?.models.find((m) => m.id === model)?.context_window;
  if (!win) return null;
  const pct = used / win;
  if (pct < 0.8) return null;
  const title = `上下文 ${fmtK(used)} / ${fmtK(win)} tokens（点开看明细）`;
  return (
    <Button size="sm" variant="ghost" onClick={() => sh.api.openPanel("usage")} title={title} aria-label={title}>
      <ProgressRing value={pct} />
    </Button>
  );
}
