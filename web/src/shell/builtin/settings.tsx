// 内置设置页（前端方案第十二节）：通用、外观、关于。都是工作台自己的界面偏好或只读的 hello 信息；
// 后端插件的配置页、快捷键改绑、通知等在第 7 步补齐。每行带 id，搜索靠页面声明的 entries 找到它。

import { Info, Palette, SlidersHorizontal } from "lucide-react";
import { useHello, useConnection, useStore } from "../../client/react";
import { KeyValue, Segmented, Select, SettingsRow, SettingsSection, Switch } from "../../ui";
import type { ContributionPack } from "../registry";
import type { ShellState } from "../context";
import type { Prefs } from "../prefs";

function usePref<K extends keyof Prefs>(sh: ShellState, k: K): [Prefs[K], (v: Prefs[K]) => void] {
  const p = useStore(sh.prefs);
  return [p[k], (v) => sh.prefs.set(k, v)];
}

function General({ sh }: { sh: ShellState }) {
  const [sendKey, setSendKey] = usePref(sh, "sendKey");
  const [locale, setLocale] = usePref(sh, "locale");
  return (
    <SettingsSection title="输入">
      <SettingsRow
        id="sendKey"
        title="发送键"
        description="另一种组合换行"
        control={
          <Segmented
            label="发送键"
            value={sendKey}
            onChange={setSendKey}
            options={[
              { value: "enter", label: "Enter" },
              { value: "mod-enter", label: "Ctrl / ⌘ + Enter" },
            ]}
          />
        }
      />
      <SettingsRow
        id="locale"
        title="语言"
        description="刷新后生效"
        control={
          <Select value={locale} onChange={(e) => setLocale(e.target.value as Prefs["locale"])} aria-label="语言">
            <option value="zh">中文</option>
            <option value="en">English</option>
          </Select>
        }
      />
    </SettingsSection>
  );
}

function Appearance({ sh }: { sh: ShellState }) {
  const [theme, setTheme] = usePref(sh, "theme");
  const [density, setDensity] = usePref(sh, "density");
  const [width, setWidth] = usePref(sh, "chatWidth");
  const [thinking, setThinking] = usePref(sh, "thinkingOpen");
  const [collapse, setCollapse] = usePref(sh, "toolsAutoCollapse");
  const [ctx, setCtx] = usePref(sh, "showContext");
  return (
    <>
      <SettingsSection title="主题">
        <SettingsRow
          id="theme"
          title="配色"
          control={
            <Segmented
              label="配色"
              value={theme}
              onChange={setTheme}
              options={[
                { value: "system", label: "跟随系统" },
                { value: "light", label: "浅色" },
                { value: "dark", label: "深色" },
              ]}
            />
          }
        />
        <SettingsRow
          id="density"
          title="密度"
          control={
            <Segmented
              label="密度"
              value={density}
              onChange={setDensity}
              options={[
                { value: "comfortable", label: "舒适" },
                { value: "compact", label: "紧凑" },
              ]}
            />
          }
        />
        <SettingsRow
          id="chatWidth"
          title="对话宽度"
          description={`${width}px`}
          control={
            <input
              type="range"
              min={560}
              max={1200}
              step={20}
              value={width}
              aria-label="对话宽度"
              onChange={(e) => setWidth(Number(e.target.value))}
            />
          }
        />
      </SettingsSection>
      <SettingsSection title="对话">
        <SettingsRow id="thinkingOpen" title="推理默认展开" control={<Switch checked={thinking} onChange={setThinking} label="推理默认展开" />} />
        <SettingsRow
          id="toolsAutoCollapse"
          title="工具步骤结束后收起"
          control={<Switch checked={collapse} onChange={setCollapse} label="工具步骤结束后收起" />}
        />
        <SettingsRow
          id="showContext"
          title="显示上下文注入"
          description="插件在每轮请求前注入的内容（记忆、技能清单等）"
          control={<Switch checked={ctx} onChange={setCtx} label="显示上下文注入" />}
        />
      </SettingsSection>
    </>
  );
}

function About() {
  const h = useHello();
  const conn = useConnection();
  return (
    <SettingsSection title="连接">
      <div style={{ padding: "12px 16px" }}>
        <KeyValue
          rows={[
            ["连接", conn],
            ["服务端", h?.server ?? "—"],
            ["协议版本", h ? String(h.protocol) : "—"],
            ["模型", h?.model || "—"],
            ["工作目录", h?.workdir ?? "—"],
            ["受信目录", h ? (h.trusted ? "是" : "否") : "—"],
            ["身份", h ? `${h.principal.id}（${h.principal.role}）` : "—"],
            ["能力", h?.capabilities.join(", ") || "—"],
          ]}
        />
      </div>
    </SettingsSection>
  );
}

export function settingsPack(sh: ShellState): ContributionPack {
  return {
    id: "settings",
    title: "内置设置页",
    builtin: true,
    setup(ctx) {
      ctx.add(sh.reg.settingsPages, {
        id: "general",
        order: 0,
        title: "通用",
        icon: <SlidersHorizontal size={14} />,
        group: "app",
        entries: [
          { id: "sendKey", title: "发送键", keywords: ["enter", "换行"] },
          { id: "locale", title: "语言", keywords: ["language"] },
        ],
        render: () => <General sh={sh} />,
      });
      ctx.add(sh.reg.settingsPages, {
        id: "appearance",
        order: 1,
        title: "外观",
        icon: <Palette size={14} />,
        group: "app",
        entries: [
          { id: "theme", title: "配色", keywords: ["主题", "深色", "浅色", "theme", "dark"] },
          { id: "density", title: "密度", keywords: ["紧凑"] },
          { id: "chatWidth", title: "对话宽度" },
          { id: "thinkingOpen", title: "推理默认展开", keywords: ["思考"] },
          { id: "toolsAutoCollapse", title: "工具步骤结束后收起" },
          { id: "showContext", title: "显示上下文注入" },
        ],
        render: () => <Appearance sh={sh} />,
      });
      ctx.add(sh.reg.settingsPages, {
        id: "about",
        order: 100,
        title: "关于",
        icon: <Info size={14} />,
        group: "about",
        entries: [{ id: "about", title: "连接与版本", keywords: ["模型", "工作目录", "协议"] }],
        render: () => <About />,
      });
    },
  };
}
