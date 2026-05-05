// SPDX-License-Identifier: MIT
// useSession — selector for one session's metadata + status. Spec
// item 19/55 (one of four colocated hooks).
import { useStore } from "zustand";
import type { DaemonStore } from "@/lib/store/daemonStore";
import type { SessionId, SessionMetadata } from "@/lib/api/types";

export interface UseSessionResult {
  session: SessionMetadata | undefined;
  /** True while the session is awaiting / streaming a turn. */
  isBusy: boolean;
}

export function useSession(store: DaemonStore, sessionId: SessionId): UseSessionResult {
  const session = useStore(store, (s) => s.sessions.byId[sessionId]);
  const isBusy = session?.status === "thinking" || session?.status === "running";
  return { session, isBusy };
}
