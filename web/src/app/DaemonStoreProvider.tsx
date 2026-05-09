// SPDX-License-Identifier: MIT
// DaemonStoreProvider — supplies a per-daemon zustand store to nested
// components. The provider holds a Map<daemonId, DaemonStore> and
// lazily creates entries the first time a component asks for one. The
// `useDaemonStore(daemonId)` hook returns the matching store; it is
// stable across renders for a given daemonId.
//
// Spec ref: web-chat-ui Spec 6 (Provider wiring). The store factory
// already lives in `lib/store/daemonStore.ts`; this file is the React
// glue that lets routing layers select stores by id.
//
// Audit ref: addresses the audit finding that the SPA never mounted
// the per-daemon store registry, leaving every store-bound component
// dark.
import { createContext, useContext, useMemo, useRef } from "react";
import type { ReactElement, ReactNode } from "react";
import { createDaemonStore, type DaemonStore } from "@/lib/store/daemonStore";
import type { DaemonId } from "@/lib/api/types";

interface Registry {
  get: (daemonId: DaemonId) => DaemonStore;
}

const DaemonStoreContext = createContext<Registry | null>(null);

export interface DaemonStoreProviderProps {
  children: ReactNode;
  /** Test injection: a pre-seeded registry. */
  registry?: Map<DaemonId, DaemonStore>;
}

export function DaemonStoreProvider({
  children,
  registry,
}: DaemonStoreProviderProps): ReactElement {
  // useRef so the registry is stable across renders. We don't use
  // useState because we never need to trigger a re-render when a new
  // store is added — components subscribe via useStore() on the
  // returned DaemonStore directly.
  const registryRef = useRef<Map<DaemonId, DaemonStore>>(
    registry ?? new Map<DaemonId, DaemonStore>(),
  );

  const value = useMemo<Registry>(
    () => ({
      get(daemonId: DaemonId): DaemonStore {
        const map = registryRef.current;
        let s = map.get(daemonId);
        if (!s) {
          s = createDaemonStore(daemonId);
          map.set(daemonId, s);
        }
        return s;
      },
    }),
    [],
  );

  return (
    <DaemonStoreContext.Provider value={value}>
      {children}
    </DaemonStoreContext.Provider>
  );
}

/**
 * Returns the zustand store for a daemon. Lazily creates one on first
 * access. The returned reference is stable for the lifetime of the
 * provider.
 */
export function useDaemonStore(daemonId: DaemonId): DaemonStore {
  const ctx = useContext(DaemonStoreContext);
  if (!ctx) {
    throw new Error(
      "useDaemonStore must be used inside <DaemonStoreProvider>",
    );
  }
  return ctx.get(daemonId);
}
