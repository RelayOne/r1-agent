// SPDX-License-Identifier: MIT
// Sibling unit tests for messagesSlice.ts — added to satisfy the
// coverage-manifest gate (every src/lib file needs a sibling test).
//
// The messagesSlice factory exposes only the empty messages record;
// the cross-slice envelope dispatcher that mutates messages lives in
// daemonStore.ts. The interesting pure logic in this file is the
// `appendPart` merger, which is exercised here.
import { describe, it, expect } from "vitest";
import type { StoreApi } from "zustand";
import {
  appendPart,
  createMessagesSlice,
  emptyMessagesState,
  messageKey,
  type MessagesSlice,
} from "@/lib/store/messagesSlice";
import type { MessagePart } from "@/lib/api/types";
import type { DaemonState } from "@/lib/store/daemonStore";

function harness(): MessagesSlice {
  const state: { current: Partial<DaemonState> } = {
    current: { messages: emptyMessagesState() },
  };
  const set: StoreApi<DaemonState>["setState"] = ((
    updater: unknown,
  ): void => {
    if (typeof updater === "function") {
      const next = (updater as (prev: DaemonState) => Partial<DaemonState>)(
        state.current as DaemonState,
      );
      state.current = { ...state.current, ...next } as Partial<DaemonState>;
      return;
    }
    state.current = { ...state.current, ...(updater as Partial<DaemonState>) };
  }) as StoreApi<DaemonState>["setState"];
  const get = (() => state.current as DaemonState) as StoreApi<DaemonState>["getState"];
  const store = {} as StoreApi<DaemonState>;
  return createMessagesSlice(set, get, store);
}

describe("messagesSlice", () => {
  it("messageKey joins session and message ids with a colon", () => {
    expect(messageKey("s1", "m1")).toBe("s1:m1");
  });

  it("emptyMessagesState returns a fresh empty record on every call", () => {
    const a = emptyMessagesState();
    const b = emptyMessagesState();
    expect(a.byKey).toEqual({});
    expect(a.orderBySession).toEqual({});
    expect(a).not.toBe(b);
  });

  it("createMessagesSlice returns a MessagesSlice with empty messages state", () => {
    const slice = harness();
    expect(typeof slice).toBe("object");
    expect(slice.messages).toEqual(emptyMessagesState());
  });

  describe("appendPart", () => {
    it("coalesces consecutive text parts into a single concatenated text", () => {
      const a: MessagePart = { kind: "text", text: "Hello, " };
      const b: MessagePart = { kind: "text", text: "world." };
      const merged = appendPart([a], b);
      expect(merged).toEqual([{ kind: "text", text: "Hello, world." }]);
    });

    it("appends a text part when the previous part is non-text", () => {
      const tool: MessagePart = {
        kind: "tool",
        toolCallId: "t1",
        toolName: "lookup",
        input: {},
        state: "input-available",
      };
      const text: MessagePart = { kind: "text", text: "ok" };
      const merged = appendPart([tool], text);
      expect(merged.length).toBe(2);
      expect(merged[1]).toEqual(text);
    });

    it("replaces a tool part with the same toolCallId in place", () => {
      const t1: MessagePart = {
        kind: "tool",
        toolCallId: "t1",
        toolName: "lookup",
        input: {},
        state: "input-available",
      };
      const t1Done: MessagePart = {
        kind: "tool",
        toolCallId: "t1",
        toolName: "lookup",
        input: {},
        output: { hit: true },
        state: "output-available",
      };
      const merged = appendPart([t1], t1Done);
      expect(merged.length).toBe(1);
      expect(merged[0]).toEqual(t1Done);
    });

    it("replaces a reasoning part in place rather than appending a duplicate", () => {
      const r1: MessagePart = { kind: "reasoning", text: "first", state: "streaming" };
      const r2: MessagePart = { kind: "reasoning", text: "second", state: "complete" };
      const merged = appendPart([r1], r2);
      expect(merged).toEqual([r2]);
    });
  });
});
