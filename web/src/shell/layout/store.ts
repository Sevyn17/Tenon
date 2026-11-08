// 布局状态的 store：reducer + 持久化（localStorage，键带命名空间）。读回时按已注册的面板清洗（插件没了的面板去掉）。
// 写入做 300ms 节流：拖分隔条时不每帧写存储。
// v2（2026-11-08）：缺省不开右侧面板；旧 v1 存档里开着的面板不再沿用，按缺省重来一次。

import { createStore, type ReadStore } from "../../client";
import { EMPTY_LAYOUT, layoutReducer, sanitize, type LayoutAction, type LayoutState } from "./reducer";

export interface LayoutStore extends ReadStore<LayoutState> {
  dispatch(a: LayoutAction): void;
  /** 已注册面板变化后重新清洗（插件停用时它的面板从布局里消失）。 */
  setKnown(ids: Set<string>): void;
  /** 恢复成某个预设的初始布局。 */
  reset(init?: LayoutState): void;
}

export function createLayoutStore(ns = "tenon", init: LayoutState = EMPTY_LAYOUT, storage: Storage | null = safe()): LayoutStore {
  const key = `${ns}.layout.v2`;
  let raw: unknown = null;
  try {
    const t = storage?.getItem(key);
    raw = t ? JSON.parse(t) : null;
  } catch {
    raw = null;
  }
  // 面板注册之前先按原样载入，setKnown 时清洗；没有存档用预设初始布局。
  const store = createStore<LayoutState>(raw ? { ...EMPTY_LAYOUT, ...(raw as LayoutState) } : init);
  let timer: ReturnType<typeof setTimeout> | null = null;
  const save = () => {
    if (!storage) return;
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => {
      try {
        storage.setItem(key, JSON.stringify(store.get()));
      } catch {
        /* 存储满或被禁用：只是不持久化 */
      }
    }, 300);
  };
  return {
    get: store.get,
    subscribe: store.subscribe,
    dispatch(a) {
      store.set(layoutReducer(store.get(), a));
      save();
    },
    setKnown(ids) {
      store.set(sanitize(store.get(), ids));
    },
    reset(next = init) {
      store.set(next);
      save();
    },
  };
}

function safe(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}
