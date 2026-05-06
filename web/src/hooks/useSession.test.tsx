// SPDX-License-Identifier: MIT
import React from "react";
import { describe, it, expect, beforeEach } from "vitest";
import { render } from "@testing-library/react";
import { useSession, type UseSessionResult } from "@/hooks/useSession";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { SessionMetadata } from "@/lib/api/types";

const NOW = "2026-05-04T12:00:00.000Z";
const NOOP = { schedule: () => 0, cancel: () => {} };

function makeSession(id: string, status: SessionMetadata["status"] = "idle"): SessionMetadata {
  return {
    id, title: id, workdir: "/tmp/wd", model: "x", status,
    createdAt: NOW, updatedAt: NOW, lastActivityAt: null,
    costUsd: 0, laneCount: 0, systemPromptPreset: null,
  };
}

function Host({ store, sid, apiRef }: {
  store: DaemonStore;
  sid: string;
  apiRef: { current: UseSessionResult | null };
}): React.ReactElement {
  apiRef.current = useSession(store, sid);
  return <div />;
}

describe("useSession", () => {
  beforeEach(() => _resetDaemonRegistryForTests());

  it("returns undefined session and isBusy=false when missing", () => {
    const store = createDaemonStore("d1", NOOP);
    const apiRef = { current: null as UseSessionResult | null };
    render(<Host store={store} sid="nope" apiRef={apiRef} />);
    expect(apiRef.current?.session).toBeUndefined();
    expect(apiRef.current?.isBusy).toStrictEqual(false);
  });

  it("returns session metadata when present", () => {
    const store = createDaemonStore("d1", NOOP);
    store.getState().hydrateSessions([makeSession("s1")]);
    const apiRef = { current: null as UseSessionResult | null };
    render(<Host store={store} sid="s1" apiRef={apiRef} />);
    expect(apiRef.current?.session?.id).toBe("s1");
    expect(apiRef.current?.isBusy).toStrictEqual(false);
  });

  it("isBusy is true when status is thinking", () => {
    const store = createDaemonStore("d1", NOOP);
    store.getState().hydrateSessions([makeSession("s1", "thinking")]);
    const apiRef = { current: null as UseSessionResult | null };
    render(<Host store={store} sid="s1" apiRef={apiRef} />);
    expect(apiRef.current?.isBusy).toStrictEqual(true);
  });

  it("isBusy is true when status is running", () => {
    const store = createDaemonStore("d1", NOOP);
    store.getState().hydrateSessions([makeSession("s1", "running")]);
    const apiRef = { current: null as UseSessionResult | null };
    render(<Host store={store} sid="s1" apiRef={apiRef} />);
    expect(apiRef.current?.isBusy).toStrictEqual(true);
  });
});
