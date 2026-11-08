// 面板入口（06 §3.1）：顶栏右侧的面板按钮，点开列出全部面板与底部停靠区。
// 取代原来的面板竖栏；面板里有内容（badge > 0）时按钮上亮一个小圆点。

import { PanelBottom, PanelRight } from "lucide-react";
import { useStore } from "../../client/react";
import { IconButton, Menu, type MenuGroupSpec, type MenuItemSpec } from "../../ui";
import { bindingOf } from "../commands";
import { useCommandContext, useRegistry, useShell } from "../context";
import { isOpen } from "./reducer";
import s from "./panelmenu.module.css";

export function PanelsMenu() {
  const sh = useShell();
  const panels = useRegistry(sh.reg.panels);
  const layout = useStore(sh.layout);
  const cx0 = useCommandContext();

  const item = (p: (typeof panels)[number]): MenuItemSpec => ({
    id: p.id,
    label: p.title,
    icon: p.icon,
    keybind: bindingOf(sh, `panel.${p.id}`) ?? p.keybind,
    checked: isOpen(layout, p.id),
    run: () => sh.api.togglePanel(p.id),
  });
  const right = panels.filter((p) => (p.dock ?? "right") === "right");
  const bottom = panels.filter((p) => p.dock === "bottom");
  const groups: MenuGroupSpec[] = [];
  if (right.length) groups.push({ id: "right", label: sh.t("panel.group.right"), items: right.map(item) });
  if (bottom.length) groups.push({ id: "bottom", label: sh.t("panel.group.bottom"), items: bottom.map(item) });
  groups.push({
    id: "dock",
    items: [
      {
        id: "bottomDock",
        label: sh.t("rail.bottom"),
        icon: <PanelBottom size={14} />,
        keybind: bindingOf(sh, "shell.toggleBottom"),
        checked: !!layout.docks.bottom && !layout.hidden.bottom,
        run: () => sh.api.run("shell.toggleBottom"),
      },
    ],
  });

  const hasContent = panels.some((p) => (p.badge?.(cx0) ?? 0) > 0);
  return (
    <Menu
      placement="bottom-end"
      groups={groups}
      trigger={({ ref, toggle }) => (
        <IconButton
          ref={ref}
          label={sh.t("rail.label")}
          keybind={bindingOf(sh, "shell.togglePanels")}
          size="sm"
          onClick={toggle}
          className={s.panelBtn}
        >
          <PanelRight size={16} />
          {hasContent && <span className={s.panelDot} aria-hidden />}
        </IconButton>
      )}
    />
  );
}
