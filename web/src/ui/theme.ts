// 主题与密度：写到 <html data-theme / data-density>，令牌（tokens.css）按属性取值。
// 主题 "system" 跟随 prefers-color-scheme，系统切换时自动跟着变。偏好由调用方保存（shell 存 localStorage）。

export type ThemePref = "system" | "light" | "dark";
export type Density = "comfortable" | "compact";

let mq: MediaQueryList | null = null;
let onChange: (() => void) | null = null;

/** 解析后的实际主题。 */
export function resolveTheme(pref: ThemePref): "light" | "dark" {
  if (pref !== "system") return pref;
  return typeof matchMedia === "function" && matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

/** 应用主题；pref = system 时监听系统变化。root 缺省为 <html>。 */
export function applyTheme(pref: ThemePref, root: HTMLElement = document.documentElement): void {
  if (mq && onChange) mq.removeEventListener("change", onChange);
  mq = null;
  onChange = null;
  root.dataset.theme = resolveTheme(pref);
  if (pref === "system" && typeof matchMedia === "function") {
    mq = matchMedia("(prefers-color-scheme: light)");
    onChange = () => {
      root.dataset.theme = resolveTheme("system");
    };
    mq.addEventListener("change", onChange);
  }
}

export function applyDensity(d: Density, root: HTMLElement = document.documentElement): void {
  root.dataset.density = d;
}
