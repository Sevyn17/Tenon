// 视口档位（前端方案第十四节"响应式"）：lg ≥ 1024 全开；md 768–1024 左栏收成图标条；sm < 768 左栏与面板区变抽屉、
// 不显示面板竖栏（面板从对话头部的菜单打开）。按 matchMedia 订阅，命令（React 之外）也能读当前档位。

import { useSyncExternalStore } from "react";

export type Viewport = "sm" | "md" | "lg";

const SM = "(max-width: 767px)";
const MD = "(max-width: 1023px)";
// 面板区什么时候改成覆盖式抽屉（06 §3.1：窗口不足 1100px）。与视口档位无关：md/lg 都可能落在抽屉里。
const PANEL_DRAWER = "(max-width: 1099px)";

export function currentViewport(): Viewport {
  if (typeof matchMedia !== "function") return "lg";
  if (matchMedia(SM).matches) return "sm";
  if (matchMedia(MD).matches) return "md";
  return "lg";
}

function subscribe(fn: () => void): () => void {
  if (typeof matchMedia !== "function") return () => {};
  const qs = [matchMedia(SM), matchMedia(MD)];
  for (const q of qs) q.addEventListener("change", fn);
  // 兜底：窗口在后台或尺寸为 0 时有的浏览器不发 change，resize 时再读一次（档位没变时 React 不重渲染）。
  addEventListener("resize", fn);
  return () => {
    for (const q of qs) q.removeEventListener("change", fn);
    removeEventListener("resize", fn);
  };
}

export function useViewport(): Viewport {
  return useSyncExternalStore(subscribe, currentViewport, () => "lg" as Viewport);
}

function currentPanelDrawer(): boolean {
  return typeof matchMedia === "function" && matchMedia(PANEL_DRAWER).matches;
}

function subscribePanelDrawer(fn: () => void): () => void {
  if (typeof matchMedia !== "function") return () => {};
  const q = matchMedia(PANEL_DRAWER);
  q.addEventListener("change", fn);
  addEventListener("resize", fn);
  return () => {
    q.removeEventListener("change", fn);
    removeEventListener("resize", fn);
  };
}

/** 窗口不足以并排显示面板区时为 true：右侧面板改成覆盖在对话上的抽屉。 */
export function usePanelDrawer(): boolean {
  return useSyncExternalStore(subscribePanelDrawer, currentPanelDrawer, () => false);
}
