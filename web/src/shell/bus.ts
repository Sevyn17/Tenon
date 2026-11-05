// 一个工作台实例内部的事件通道（不走 window：同一页面挂多个工作台时互不串扰）。
// 只放区域之间的"请求"，状态一律走 store。

export interface ShellEvents {
  /** 往某会话的输入框插入文本（session 为 "*" 时插入当前输入框）。 */
  insert: { session: string; text: string };
  /** 聚焦当前输入框。 */
  focusComposer: Record<string, never>;
  /** 打开对话内查找（⌘F）。 */
  find: Record<string, never>;
  /** 跳到上 / 下一回合（⌥↑ / ⌥↓）。 */
  jumpTurn: { dir: -1 | 1 };
}

export interface ShellBus {
  emit<K extends keyof ShellEvents>(k: K, v: ShellEvents[K]): void;
  on<K extends keyof ShellEvents>(k: K, f: (v: ShellEvents[K]) => void): () => void;
}

export function createShellBus(): ShellBus {
  const subs = new Map<string, Set<(v: unknown) => void>>();
  return {
    emit(k, v) {
      for (const f of [...(subs.get(k) ?? [])]) f(v);
    },
    on(k, f) {
      let set = subs.get(k);
      if (!set) subs.set(k, (set = new Set()));
      const g = f as (v: unknown) => void;
      set.add(g);
      return () => set!.delete(g);
    },
  };
}
