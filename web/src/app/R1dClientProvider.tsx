// SPDX-License-Identifier: MIT
// R1dClientProvider — supplies one R1dClient per daemon, keyed by
// daemonId. The provider builds clients on demand from the daemon
// registry (`daemonRegistry.ts`), so route components only need to
// know an id. Consumers read a client via `useR1dClient(daemonId)`.
//
// Spec ref: web-chat-ui Spec 6 (R1dClient wiring). Pairs with the
// DaemonStoreProvider so a `<SessionView>` can build its useChat /
// useDaemonSocket hooks from the same id used in the URL.
//
// Audit ref: addresses the audit finding that no shipping wrapper
// constructed an R1dClient — every API surface stayed unreachable
// from the rendered tree.
import { createContext, useContext, useMemo, useRef } from "react";
import type { ReactElement, ReactNode } from "react";
import { R1dClient } from "@/lib/api/r1d";
import { loadRegistry, type RegistryDaemon } from "@/app/daemonRegistry";
import type { DaemonId } from "@/lib/api/types";

interface Registry {
  get: (daemonId: DaemonId) => R1dClient;
  getInfo: (daemonId: DaemonId) => RegistryDaemon | null;
}

const R1dClientContext = createContext<Registry | null>(null);

export interface R1dClientProviderProps {
  children: ReactNode;
  /** Override daemons (tests, SSR seeding). When omitted we read the
   *  current registry from localStorage at provider mount. */
  daemons?: RegistryDaemon[];
  /** Test injection: a factory that returns a pre-built client for an
   *  id. When supplied, it overrides the default constructor. */
  factory?: (info: RegistryDaemon) => R1dClient;
}

export function R1dClientProvider({
  children,
  daemons,
  factory,
}: R1dClientProviderProps): ReactElement {
  // Pin the daemon list once at mount so adding a daemon at runtime
  // doesn't tear down existing clients. The DaemonsLanding route
  // navigates after persisting; the new client is built on the next
  // page render under the new provider tree.
  const seed = daemons ?? loadRegistry();

  const infoMap = useRef<Map<DaemonId, RegistryDaemon>>(
    new Map(seed.map((d) => [d.id, d])),
  );
  const clients = useRef<Map<DaemonId, R1dClient>>(new Map());

  const value = useMemo<Registry>(() => {
    const build = (info: RegistryDaemon): R1dClient => {
      if (factory) return factory(info);
      return new R1dClient({
        baseUrl: info.baseUrl,
        wsUrl: info.wsUrl,
      });
    };
    return {
      get(daemonId: DaemonId): R1dClient {
        const cached = clients.current.get(daemonId);
        if (cached) return cached;
        const info = infoMap.current.get(daemonId);
        if (!info) {
          throw new Error(
            `useR1dClient: unknown daemonId "${daemonId}". Add it to the registry first.`,
          );
        }
        const c = build(info);
        clients.current.set(daemonId, c);
        return c;
      },
      getInfo(daemonId: DaemonId): RegistryDaemon | null {
        return infoMap.current.get(daemonId) ?? null;
      },
    };
  }, [factory]);

  return (
    <R1dClientContext.Provider value={value}>
      {children}
    </R1dClientContext.Provider>
  );
}

export function useR1dClient(daemonId: DaemonId): R1dClient {
  const ctx = useContext(R1dClientContext);
  if (!ctx) {
    throw new Error(
      "useR1dClient must be used inside <R1dClientProvider>",
    );
  }
  return ctx.get(daemonId);
}

export function useR1dClientInfo(
  daemonId: DaemonId,
): RegistryDaemon | null {
  const ctx = useContext(R1dClientContext);
  if (!ctx) return null;
  return ctx.getInfo(daemonId);
}
