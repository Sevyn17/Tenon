// ⑨ 设置（前端方案第十二节）：覆盖层，左侧分节导航（按 group 分组：工作台 / 后端与插件 / 插件页 / 关于）+ 搜索，
// 右侧是页面本身与贡献到该页的区块（SettingsSection）。页面、区块都来自注册表，插件停用时一起消失。
// 搜索只看各页声明的 entries，不需要挂载页面；搜到后跳页并按 data-setting-id 高亮那一行。

import { useEffect, useMemo, useRef, useState } from "react";
import { Search, X } from "lucide-react";
import { useStore } from "../../client/react";
import { EmptyState, ErrorBoundary, IconButton, cx } from "../../ui";
import { useRegistry, useShell } from "../context";
import type { SettingsPage } from "../contributions";
import s from "./settings.module.css";

const GROUPS: { id: SettingsPage["group"]; label: string }[] = [
  { id: "app", label: "settings.group.app" },
  { id: "backend", label: "settings.group.backend" },
  { id: "plugin", label: "settings.group.plugin" },
  { id: "about", label: "" },
];

export function Settings() {
  const sh = useShell();
  const nav = useStore(sh.nav);
  const pages = useRegistry(sh.reg.settingsPages);
  const sections = useRegistry(sh.reg.settingsSections);
  const [q, setQ] = useState("");
  const box = useRef<HTMLDivElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const cur = nav.settings;

  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    box.current?.querySelector<HTMLInputElement>("input")?.focus();
    return () => prev?.focus?.();
  }, []);

  // 跳到某一行：等页面渲染后定位并高亮。
  useEffect(() => {
    if (!cur?.entry) return;
    const t = setTimeout(() => {
      const el = body.current?.querySelector<HTMLElement>(`[data-setting-id="${CSS.escape(cur.entry!)}"]`);
      if (!el) return;
      el.scrollIntoView({ block: "center" });
      el.dataset.highlight = "true";
      setTimeout(() => delete el.dataset.highlight, 1600);
    }, 30);
    return () => clearTimeout(t);
  }, [cur?.page, cur?.entry]);

  const hits = useMemo(() => {
    const needle = q.trim().toLowerCase();
    if (!needle) return null;
    const out: { page: SettingsPage; id: string; title: string }[] = [];
    const match = (e: { title: string; keywords?: string[] }) =>
      e.title.toLowerCase().includes(needle) || (e.keywords ?? []).some((k) => k.toLowerCase().includes(needle));
    for (const p of pages) {
      if (p.title.toLowerCase().includes(needle)) out.push({ page: p, id: "", title: p.title });
      for (const e of p.entries ?? []) if (match(e)) out.push({ page: p, id: e.id, title: e.title });
      for (const sec of sections)
        if (sec.page === p.id) for (const e of sec.entries ?? []) if (match(e)) out.push({ page: p, id: e.id, title: e.title });
    }
    return out;
  }, [q, pages, sections]);

  if (!cur) return null;
  const page = pages.find((p) => p.id === cur.page) ?? pages[0];
  const go = (id: string, entry?: string) => sh.api.openSettings(id, entry);
  const close = () => sh.api.closeSettings();
  const own = sections.filter((x) => x.page === page?.id);

  return (
    <div className={s.scrim} onMouseDown={(e) => e.target === e.currentTarget && close()}>
      <div
        ref={box}
        className={s.box}
        role="dialog"
        aria-modal="true"
        aria-label={sh.t("settings.title")}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.stopPropagation();
            if (q) setQ("");
            else close();
          }
        }}
      >
        <nav className={s.nav}>
          <div className={s.search}>
            <Search size={14} />
            <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={sh.t("settings.search")} aria-label={sh.t("settings.search")} />
          </div>
          {hits ? (
            <div className={s.navList}>
              {hits.length === 0 && <div className={s.navEmpty}>{sh.t("settings.noMatch")}</div>}
              {hits.map((h) => (
                <button
                  key={h.page.id + "/" + h.id}
                  type="button"
                  className={s.navItem}
                  onClick={() => {
                    setQ("");
                    go(h.page.id, h.id || undefined);
                  }}
                >
                  <span className={s.grow}>{h.title}</span>
                  {h.id && <span className={s.navHint}>{h.page.title}</span>}
                </button>
              ))}
            </div>
          ) : (
            <div className={s.navList}>
              {GROUPS.map((g) => {
                const ps = pages.filter((p) => p.group === g.id);
                if (!ps.length) return null;
                return (
                  <div key={g.id} className={s.navGroup}>
                    {g.label && <div className={s.navGroupLabel}>{sh.t(g.label)}</div>}
                    {ps.map((p) => (
                      <button
                        key={p.id}
                        type="button"
                        className={cx(s.navItem, p.id === page?.id && s.navActive)}
                        aria-current={p.id === page?.id ? "page" : undefined}
                        onClick={() => go(p.id)}
                      >
                        {p.icon}
                        <span className={s.grow}>{p.title}</span>
                      </button>
                    ))}
                  </div>
                );
              })}
            </div>
          )}
        </nav>
        <div className={s.main}>
          <header className={s.head}>
            <h2>{page?.title ?? sh.t("settings.title")}</h2>
            <IconButton label={sh.t("common.close")} size="sm" onClick={close}>
              <X size={16} />
            </IconButton>
          </header>
          <div ref={body} className={s.body}>
            {!page ? (
              <EmptyState title={sh.t("settings.noPages")} />
            ) : (
              <ErrorBoundary label={`设置 ${page.title}`} resetKey={page.id} onError={(e) => page.pack && sh.packs.fail(page.pack, e.message)}>
                {page.render?.()}
                {own.map((sec) => (
                  <ErrorBoundary key={sec.id} label={sec.title} onError={(e) => sec.pack && sh.packs.fail(sec.pack, e.message)}>
                    {sec.render()}
                  </ErrorBoundary>
                ))}
                {!page.render && own.length === 0 && <EmptyState title={sh.t("settings.emptyPage")} />}
              </ErrorBoundary>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
