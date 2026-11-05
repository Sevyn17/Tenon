// 命令与快捷键（前端方案第十三节）：快捷键是命令的字段；用户改绑存在 prefs.keybinds（"" = 解绑）。
// 全局按键分发：输入框聚焦时只有 global 命令生效（其余让路给输入框）；有模态框时不分发（模态框自己处理）。

import { useEffect } from "react";
import type { Command, CommandContext } from "./contributions";
import type { ShellState } from "./context";
import { createDispatcher } from "./keys";

/** 命令当前生效的快捷键（改绑优先，"" = 无）。 */
export function bindingOf(sh: Pick<ShellState, "reg" | "prefs">, id: string): string | undefined {
  const over = sh.prefs.get().keybinds[id];
  if (over !== undefined) return over || undefined;
  return sh.reg.commands.byId(id)?.keybind;
}

/** 全部命令 → 生效的快捷键表（只含有快捷键的）。 */
export function effectiveBindings(sh: Pick<ShellState, "reg" | "prefs">): Map<string, string> {
  const m = new Map<string, string>();
  for (const c of sh.reg.commands.get()) {
    const b = bindingOf(sh, c.id);
    if (b) m.set(c.id, b);
  }
  return m;
}

/** 执行命令：when 不满足时不执行，返回原因。 */
export async function runCommand(c: Command, cx: CommandContext): Promise<void | false | string> {
  const w = c.when?.(cx);
  if (w === false) return "当前不可用";
  if (typeof w === "string") return w;
  return c.run(cx);
}

function inEditable(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el) return false;
  return el.isContentEditable || el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT";
}

/** 挂到窗口上的快捷键分发。 */
export function useKeybindings(sh: ShellState, mac: boolean) {
  useEffect(() => {
    const d = createDispatcher(mac);
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.isComposing) return;
      if (document.querySelector('[aria-modal="true"]')) return; // 模态框里按键归模态框
      const editable = inEditable(e.target);
      const all = effectiveBindings(sh);
      // 输入框里只考虑 global 命令；其余命令的键原样交给输入框。
      const usable = new Map<string, string>();
      for (const [id, b] of all) {
        const c = sh.reg.commands.byId(id);
        if (!c) continue;
        // 只用修饰键的组合（Mod+…）在输入框里也生效；单字符键（?、a）在输入框里让路。
        if (editable && !c.global && !/Mod|Ctrl|Alt/.test(b)) continue;
        usable.set(id, b);
      }
      const id = d.match(usable, e);
      if (!id) return;
      const c = sh.reg.commands.byId(id);
      if (!c) return;
      const cx: CommandContext = { client: sh.client, session: sh.api.current(), shell: sh.api };
      const w = c.when?.(cx);
      if (w === false || typeof w === "string") return;
      const r = c.run(cx);
      if (r === false) return; // 让给焦点元素
      e.preventDefault();
      e.stopPropagation();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [sh, mac]);
}
