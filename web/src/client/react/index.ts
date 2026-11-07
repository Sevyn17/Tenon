// @tenon/client/react：无样式的 React hooks。只依赖 client 与 react，可以配任何组件库使用。

import { createContext, createElement, useContext, useEffect, useSyncExternalStore, type ReactNode } from "react";
import type { Client } from "../index";
import type { ReadStore } from "../store";
import type { SessionStore } from "../session";

const Ctx = createContext<Client | null>(null);

export function ClientProvider({ client, children }: { client: Client; children: ReactNode }) {
  return createElement(Ctx.Provider, { value: client }, children);
}

export function useClient(): Client {
  const c = useContext(Ctx);
  if (!c) throw new Error("useClient 须在 <ClientProvider> 内使用");
  return c;
}

/** 订阅任意 ReadStore。 */
export function useStore<T>(s: ReadStore<T>): T {
  return useSyncExternalStore(s.subscribe, s.get, s.get);
}

export function useHello() {
  return useStore(useClient().hello);
}

export function useConnection() {
  return useStore(useClient().conn.state);
}

export function useSessions() {
  return useStore(useClient().sessions);
}

export function useInteractions() {
  return useStore(useClient().interactions);
}

/** 打开某会话并订阅；组件卸载或 id 变化时取消订阅。返回 [状态, store]。 */
export function useSession(id: string, opts?: { create?: boolean }): [ReturnType<SessionStore["get"]>, SessionStore] {
  const client = useClient();
  const store = client.session(id);
  const create = !!opts?.create;
  useEffect(() => {
    void store.open({ create });
    return () => store.close();
  }, [store, create]);
  return [useStore(store), store];
}
