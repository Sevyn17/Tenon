// 设置页构件（参照 Codex 桌面版：Section / Card / Row）。核心设置与插件贡献的设置都用它们，外观一致。
// 每行带 id：设置搜索跳到某行时按 data-setting-id 定位并高亮。

import type { ReactNode } from "react";
import { cx } from "./cx";
import s from "./settings.module.css";

export function SettingsSection(p: { title?: ReactNode; description?: ReactNode; children: ReactNode; id?: string }) {
  return (
    <section className={s.section} data-setting-section={p.id}>
      {(p.title || p.description) && (
        <div className={s.sectionHead}>
          {p.title && <h3 className={s.sectionTitle}>{p.title}</h3>}
          {p.description && <p className={s.sectionDesc}>{p.description}</p>}
        </div>
      )}
      <div className={s.card}>{p.children}</div>
    </section>
  );
}

export function SettingsRow(p: {
  id?: string;
  title: ReactNode;
  description?: ReactNode;
  control?: ReactNode;
  required?: boolean;
  /** 控件放在说明下方（宽控件，如列表、文本框）。 */
  stacked?: boolean;
  highlight?: boolean;
}) {
  return (
    <div className={cx(s.row, p.stacked && s.rowStack)} data-setting-id={p.id} data-highlight={p.highlight ? "true" : undefined}>
      <div className={s.rowText}>
        <div className={s.rowTitle}>
          {p.title}
          {p.required && <span className={s.req}>*</span>}
        </div>
        {p.description && <div className={s.rowDesc}>{p.description}</div>}
      </div>
      {p.control && <div className={s.rowControl}>{p.control}</div>}
    </div>
  );
}

/** 卡片底部的说明行（如只读原因）。 */
export function SettingsNote({ children }: { children: ReactNode }) {
  return <div className={s.note}>{children}</div>;
}
