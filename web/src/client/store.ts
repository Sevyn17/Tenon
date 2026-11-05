// 极小的可订阅 store：get / set / subscribe。React 端用 useSyncExternalStore 接上（client/react），
// 其他框架（Vue、原生 JS）直接 subscribe。值约定不可变：set 传新对象，订阅方按引用比较。

export interface ReadStore<T> {
  get(): T;
  subscribe(fn: (v: T) => void): () => void;
}

export interface Store<T> extends ReadStore<T> {
  set(v: T): void;
  update(f: (v: T) => T): void;
}

export function createStore<T>(init: T): Store<T> {
  let v = init;
  const subs = new Set<(v: T) => void>();
  return {
    get: () => v,
    set(nv) {
      if (Object.is(nv, v)) return;
      v = nv;
      for (const s of [...subs]) s(v);
    },
    update(f) {
      this.set(f(v));
    },
    subscribe(fn) {
      subs.add(fn);
      return () => subs.delete(fn);
    },
  };
}

/** 派生 store：从一个 store 的值算出新值（只在源变化时重算）。 */
export function derive<A, B>(src: ReadStore<A>, f: (a: A) => B): ReadStore<B> {
  let last = src.get();
  let cached = f(last);
  return {
    get() {
      const cur = src.get();
      if (cur !== last) {
        last = cur;
        cached = f(cur);
      }
      return cached;
    },
    subscribe(fn) {
      return src.subscribe(() => fn(this.get()));
    },
  };
}
