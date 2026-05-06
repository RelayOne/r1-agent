// SPDX-License-Identifier: MIT
// Tests for useChat — the hook that exposes a useChat-shaped surface
// over our zustand store + WS transport.
import React from "react";
import { describe, it, expect, beforeEach } from "vitest";
import { render, act } from "@testing-library/react";
import { useChat, type UseChatHelpers } from "@/hooks/useChat";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { SessionMetadata } from "@/lib/api/types";

const NOW = "2026-05-04T12:00:00.000Z";
const NOOP = { schedule: () => 0, cancel: () => {} };

function seedSession(store: DaemonStore, id: string, status: SessionMetadata["status"] = "idle"): void {
  store.getState().hydrateSessions([
    {
      id,
      title: id,
      workdir: "/tmp/wd",
      model: "claude",
      status,
      createdAt: NOW,
      updatedAt: NOW,
      lastActivityAt: null,
      costUsd: 0,
      laneCount: 0,
      systemPromptPreset: null,
    },
  ]);
}

interface HostProps {
  store: DaemonStore;
  sessionId: string;
  sendChat: (sid: string, content: string) => void;
  sendInterrupt: (sid: string) => void;
  apiRef: { current: UseChatHelpers | null };
}

function Host({ store, sessionId, sendChat, sendInterrupt, apiRef }: HostProps): React.ReactElement {
  const helpers = useChat({ store, sessionId, sendChat, sendInterrupt });
  apiRef.current = helpers;
  return <div />;
}

describe("useChat", () => {
  beforeEach(() => _resetDaemonRegistryForTests());

  it("returns no messages for a session with no history", () => {
    const store = createDaemonStore("d1", NOOP);
    seedSession(store, "s1");
    const sent: string[] = [];
    const apiRef: HostProps["apiRef"] = { current: null };
    render(<Host
      store={store}
      sessionId="s1"
      sendChat={(_sid, c) => sent.push(c)}
      sendInterrupt={() => {}}
      apiRef={apiRef}
    />);
    expect(apiRef.current?.messages).toEqual([]);
    expect(apiRef.current?.status).toBe("ready");
  });

  it("sendMessage forwards content to sendChat", () => {
    const store = createDaemonStore("d1", NOOP);
    seedSession(store, "s1");
    const sent: Array<{ sid: string; content: string }> = [];
    const apiRef: HostProps["apiRef"] = { current: null };
    render(<Host
      store={store}
      sessionId="s1"
      sendChat={(sid, content) => sent.push({ sid, content })}
      sendInterrupt={() => {}}
      apiRef={apiRef}
    />);
    act(() => apiRef.current!.sendMessage("hello"));
    expect(sent).toEqual([{ sid: "s1", content: "hello" }]);
  });

  it("sendMessage ignores empty / whitespace-only content", () => {
    const store = createDaemonStore("d1", NOOP);
    seedSession(store, "s1");
    const sent: string[] = [];
    const apiRef: HostProps["apiRef"] = { current: null };
    render(<Host
      store={store}
      sessionId="s1"
      sendChat={(_sid, c) => sent.push(c)}
      sendInterrupt={() => {}}
      apiRef={apiRef}
    />);
    act(() => apiRef.current!.sendMessage(""));
    act(() => apiRef.current!.sendMessage("   "));
    expect(sent).toEqual([]);
  });

  it("stop calls sendInterrupt with the session id", () => {
    const store = createDaemonStore("d1", NOOP);
    seedSession(store, "s1");
    const interrupts: string[] = [];
    const apiRef: HostProps["apiRef"] = { current: null };
    render(<Host
      store={store}
      sessionId="s1"
      sendChat={() => {}}
      sendInterrupt={(sid) => interrupts.push(sid)}
      apiRef={apiRef}
    />);
    act(() => apiRef.current!.stop());
    expect(interrupts).toEqual(["s1"]);
  });

  it("status reflects session status: thinking + last streaming -> 'streaming'", () => {
    const apiRef: HostProps["apiRef"] = { current: null };
    const store = createDaemonStore("d1", NOOP);
    seedSession(store, "s1", "thinking");
    expect(store.getState().sessions.byId.s1?.status).toBe("thinking");
    store.getState().applyEnvelope({
      type: "message.part",
      seq: 1,
      ts: NOW,
      sessionId: "s1",
      messageId: "m1",
      role: "assistant",
      part: { kind: "text", text: "hi" },
    });
    store.flushPending();
    render(<Host store={store} sessionId="s1" sendChat={() => {}} sendInterrupt={() => {}} apiRef={apiRef} />);
    expect(apiRef.current?.status).toBe("streaming");
  });

  it("status thinking + no parts -> 'submitted'", () => {
    const store = createDaemonStore("d1", NOOP);
    seedSession(store, "s1", "thinking");
    const apiRef: HostProps["apiRef"] = { current: null };
    render(<Host
      store={store}
      sessionId="s1"
      sendChat={() => {}}
      sendInterrupt={() => {}}
      apiRef={apiRef}
    />);
    expect(apiRef.current?.status).toBe("submitted");
  });

  it("status mirrors session.status='error'", () => {
    const store = createDaemonStore("d1", NOOP);
    seedSession(store, "s1", "error");
    const apiRef: HostProps["apiRef"] = { current: null };
    render(<Host
      store={store}
      sessionId="s1"
      sendChat={() => {}}
      sendInterrupt={() => {}}
      apiRef={apiRef}
    />);
    expect(apiRef.current?.status).toBe("error");
  });

  it("messages reflect store updates after a flush", () => {
    const store = createDaemonStore("d1", NOOP);
    seedSession(store, "s1", "thinking");
    const apiRef: HostProps["apiRef"] = { current: null };
    render(<Host
      store={store}
      sessionId="s1"
      sendChat={() => {}}
      sendInterrupt={() => {}}
      apiRef={apiRef}
    />);
    expect(apiRef.current?.messages).toHaveLength(0);
    act(() => {
      store.getState().applyEnvelope({
        type: "message.part",
        seq: 1,
        ts: NOW,
        sessionId: "s1",
        messageId: "m1",
        role: "assistant",
        part: { kind: "text", text: "hello" },
      });
      store.getState().applyEnvelope({
        type: "message.complete",
        seq: 2,
        ts: NOW,
        sessionId: "s1",
        messageId: "m1",
      });
    });
    expect(apiRef.current?.messages).toHaveLength(1);
    expect(apiRef.current?.messages[0].parts).toEqual([{ kind: "text", text: "hello" }]);
  });
});
