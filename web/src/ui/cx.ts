/** 拼类名：忽略假值。 */
export function cx(...xs: (string | false | null | undefined)[]): string {
  return xs.filter(Boolean).join(" ");
}

/** 是否 Mac（快捷键显示 ⌘ 还是 Ctrl）。 */
export const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

/** 把 "Mod+Shift+K" 这样的快捷键写法按平台显示成按键序列。 */
export function keyLabels(bind: string): string[] {
  return bind.split("+").map((k) => {
    switch (k.toLowerCase()) {
      case "mod":
        return isMac ? "⌘" : "Ctrl";
      case "shift":
        return isMac ? "⇧" : "Shift";
      case "alt":
        return isMac ? "⌥" : "Alt";
      case "ctrl":
        return isMac ? "⌃" : "Ctrl";
      case "enter":
        return "↵";
      case "escape":
      case "esc":
        return "Esc";
      case "arrowup":
        return "↑";
      case "arrowdown":
        return "↓";
      case "backslash":
        return "\\";
      default:
        return k.length === 1 ? k.toUpperCase() : k;
    }
  });
}
