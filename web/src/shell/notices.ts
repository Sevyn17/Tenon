// 通知中心的数据：最近 50 条通知（新的在前），带已读状态。api.notify 与后台运行感知（notify.ts）都写这里，
// 通知面板与竖栏角标读这里。

import { createStore, type ReadStore } from "../client";
import type { ShellNotice } from "./contributions";

const KEEP = 50;

export interface NoticesStore extends ReadStore<ShellNotice[]> {
  /** 记一条；read 为真时直接记成已读（用户正看着的会话）。 */
  add(n: Omit<ShellNotice, "id" | "at" | "read">, read?: boolean): ShellNotice;
  markAllRead(): void;
  remove(id: string): void;
  clear(): void;
  unread(): number;
}

export function createNotices(): NoticesStore {
  const store = createStore<ShellNotice[]>([]);
  let seq = 0;
  return {
    get: store.get,
    subscribe: store.subscribe,
    add(n, read = false) {
      const x: ShellNotice = { ...n, id: `n${Date.now().toString(36)}${(seq++).toString(36)}`, at: Date.now(), read };
      store.update((v) => [x, ...v].slice(0, KEEP));
      return x;
    },
    markAllRead() {
      if (store.get().every((x) => x.read)) return;
      store.update((v) => v.map((x) => (x.read ? x : { ...x, read: true })));
    },
    remove(id) {
      store.update((v) => v.filter((x) => x.id !== id));
    },
    clear() {
      store.set([]);
    },
    unread: () => store.get().filter((x) => !x.read).length,
  };
}
