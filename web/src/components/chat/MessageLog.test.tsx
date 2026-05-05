// SPDX-License-Identifier: MIT
import { describe, it, expect, beforeEach } from "vitest";
import { render, act, screen } from "@testing-library/react";
import { MessageLog } from "@/components/chat/MessageLog";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type ChatMessage,
  type DaemonStore,
} from "@/lib/store/daemonStore";

const NOOP = { schedule: () => 0, cancel: () => {} };
const NOW = "2026-05-04T12:30:00.000Z";

function mkMessage(over: Partial<ChatMessage> & { id: string }): ChatMessage {
  const { id, ...rest } = over;
  return {
    id,
    sessionId: "s-1",
    role: "assistant",
    parts: [{ kind: "text" as const, text: `body for ${id}` }],
    streaming: false,
    createdAt: NOW,
    updatedAt: NOW,
    ...rest,
  } as ChatMessage;
}

function seedMessages(store: DaemonStore, msgs: ChatMessage[]): void {
  // Bypass the WS envelope path — we drive the store directly so the
  // MessageLog test stays isolated from the protocol layer.
  const sid = "s-1";
  for (const m of msgs) {
    store.setState((prev) => {
      const order = prev.messages.orderBySession[sid] ?? [];
      return {
        ...prev,
        messages: {
          byKey: { ...prev.messages.byKey, [`${sid}:${m.id}`]: m },
          orderBySession: {
            ...prev.messages.orderBySession,
            [sid]: order.includes(m.id) ? order : [...order, m.id],
          },
        },
      };
    });
  }
}

describe("<MessageLog>", () => {
  let store: DaemonStore;
  beforeEach(() => {
    _resetDaemonRegistryForTests();
    store = createDaemonStore("d1", NOOP);
  });

  it("shows the empty-state notice when no messages exist", () => {
    render(
      <MessageLog
        store={store}
        sessionId="s-1"
        renderMessage={(m) => <div>{m.id}</div>}
      />,
    );
    expect(screen.getByTestId("message-log-empty")).toBeTruthy();
    expect(screen.getByTestId("message-log").getAttribute("aria-live")).toBe(
      "polite",
    );
  });

  it("renders a row per message in store order", () => {
    seedMessages(store, [
      mkMessage({ id: "m1" }),
      mkMessage({ id: "m2" }),
      mkMessage({ id: "m3" }),
    ]);
    render(
      <MessageLog
        store={store}
        sessionId="s-1"
        renderMessage={(m) => <span>{m.id}</span>}
        __testForceList
      />,
    );
    const rows = screen.getAllByTestId(/^message-row-/);
    expect(rows.length).toBe(3);
    expect(rows[0].getAttribute("data-testid")).toBe("message-row-m1");
    expect(rows[2].getAttribute("data-testid")).toBe("message-row-m3");
  });

  it("marks only the latest streaming row as aria-live=polite", () => {
    seedMessages(store, [
      mkMessage({ id: "m1", streaming: false }),
      mkMessage({ id: "m2", streaming: true }),
      mkMessage({ id: "m3", streaming: false }),
    ]);
    render(
      <MessageLog
        store={store}
        sessionId="s-1"
        renderMessage={(m) => <span>{m.id}</span>}
        __testForceList
      />,
    );
    const m1 = screen.getByTestId("message-row-m1");
    const m2 = screen.getByTestId("message-row-m2");
    const m3 = screen.getByTestId("message-row-m3");
    expect(m1.getAttribute("aria-live")).toBeNull();
    expect(m2.getAttribute("aria-live")).toBe("polite");
    expect(m2.getAttribute("aria-busy")).toBe("true");
    expect(m3.getAttribute("aria-live")).toBeNull();
  });

  it("releases the stuck-bottom flag when the user scrolls up past the threshold", () => {
    seedMessages(store, [mkMessage({ id: "m1" })]);
    const { container } = render(
      <MessageLog
        store={store}
        sessionId="s-1"
        renderMessage={(m) => <span>{m.id}</span>}
        __testForceList
      />,
    );
    const log = container.querySelector(
      "[data-testid='message-log']",
    ) as HTMLElement;
    expect(log.getAttribute("data-stuck-bottom")).toBe("true");
    Object.defineProperty(log, "scrollHeight", { value: 1000, configurable: true });
    Object.defineProperty(log, "clientHeight", { value: 200, configurable: true });
    Object.defineProperty(log, "scrollTop", { value: 400, configurable: true });
    act(() => {
      log.dispatchEvent(new Event("scroll"));
    });
    expect(log.getAttribute("data-stuck-bottom")).toBe("false");
  });

  it("isolates messages per session", () => {
    seedMessages(store, [mkMessage({ id: "m1" })]);
    render(
      <MessageLog
        store={store}
        sessionId="s-other"
        renderMessage={(m) => <span>{m.id}</span>}
      />,
    );
    expect(screen.getByTestId("message-log-empty")).toBeTruthy();
  });
});
