// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useEffect, useState } from "react";
import { MessageLog } from "./MessageLog";
import {
  createDaemonStore,
  type ChatMessage,
  type DaemonStore,
} from "@/lib/store/daemonStore";

const NOOP = { schedule: () => 0, cancel: () => {} };
const NOW = "2026-05-04T12:30:00.000Z";

function mkStore(): DaemonStore {
  return createDaemonStore("daemon-storybook", NOOP);
}

function pushMessage(store: DaemonStore, m: ChatMessage): void {
  store.setState((prev) => {
    const order = prev.messages.orderBySession["s-1"] ?? [];
    return {
      ...prev,
      messages: {
        byKey: { ...prev.messages.byKey, [`s-1:${m.id}`]: m },
        orderBySession: {
          ...prev.messages.orderBySession,
          "s-1": order.includes(m.id) ? order : [...order, m.id],
        },
      },
    };
  });
}

function seed(store: DaemonStore, n: number): void {
  for (let i = 1; i <= n; i += 1) {
    pushMessage(store, {
      id: `m${i}`,
      sessionId: "s-1",
      role: i % 2 === 0 ? "assistant" : "user",
      parts: [
        {
          kind: "text",
          text:
            i % 2 === 0
              ? `Reply ${i}: shadcn dialogs render fine in jsdom now that we render Radix as portals.`
              : `Question ${i}: how do I run vitest with the Tailwind class registry?`,
        },
      ],
      streaming: false,
      createdAt: NOW,
      updatedAt: NOW,
    });
  }
}

function renderMessage(m: ChatMessage, isStreaming: boolean): JSX.Element {
  return (
    <article
      className={`mx-3 my-2 rounded-md border p-3 ${
        m.role === "assistant" ? "bg-muted/40" : ""
      }`}
    >
      <header className="text-xs text-muted-foreground mb-1">
        {m.role} · {m.id}
        {isStreaming ? " · streaming" : ""}
      </header>
      <p className="text-sm">
        {m.parts.map((p) => (p.kind === "text" ? p.text : "")).join("")}
      </p>
    </article>
  );
}

function Demo({ count }: { count: number }): JSX.Element {
  const [store] = useState(() => {
    const s = mkStore();
    seed(s, count);
    return s;
  });
  return (
    <div className="h-[480px] w-[640px] border border-border rounded-md overflow-hidden bg-background flex flex-col">
      <MessageLog
        store={store}
        sessionId="s-1"
        renderMessage={renderMessage}
      />
    </div>
  );
}

function StreamingDemo(): JSX.Element {
  const [store] = useState(mkStore);
  useEffect(() => {
    seed(store, 3);
    pushMessage(store, {
      id: "stream",
      sessionId: "s-1",
      role: "assistant",
      parts: [{ kind: "text" as const, text: "Streaming reply…" }],
      streaming: true,
      createdAt: NOW,
      updatedAt: NOW,
    });
  }, [store]);
  return (
    <div className="h-[400px] w-[640px] border border-border rounded-md overflow-hidden bg-background flex flex-col">
      <MessageLog
        store={store}
        sessionId="s-1"
        renderMessage={renderMessage}
      />
    </div>
  );
}

const meta: Meta<typeof MessageLog> = {
  title: "chat/MessageLog",
  component: MessageLog,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof MessageLog>;

export const Empty: Story = {
  render: () => <Demo count={0} />,
};

export const Short: Story = {
  render: () => <Demo count={6} />,
};

export const Long: Story = {
  render: () => <Demo count={120} />,
};

export const Streaming: Story = {
  render: () => <StreamingDemo />,
};
