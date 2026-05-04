// SPDX-License-Identifier: MIT
import React from "react";
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MessageBubble } from "@/components/chat/MessageBubble";
import type { ChatMessage } from "@/lib/store/daemonStore";

vi.mock("streamdown", () => {
  return {
    Streamdown: (props: { children?: unknown }) => (
      <div data-testid="streamdown-stub">{String(props.children ?? "")}</div>
    ),
    ShikiThemeContext: React.createContext<["github-light", "github-dark"]>([
      "github-light",
      "github-dark",
    ]),
  };
});

const NOW = "2026-05-04T12:30:00.000Z";

function mkMsg(over: Partial<ChatMessage> & { id: string }): ChatMessage {
  const { id, ...rest } = over;
  return {
    id,
    sessionId: "s-1",
    role: "assistant",
    parts: [{ kind: "text" as const, text: "hello" }],
    streaming: false,
    createdAt: NOW,
    updatedAt: NOW,
    ...rest,
  } as ChatMessage;
}

const renderTool = vi.fn(() => <div data-testid="tool-card-stub">tool</div>);
const renderReasoning = vi.fn(() => (
  <div data-testid="reasoning-card-stub">reasoning</div>
));
const renderPlan = vi.fn(() => <div data-testid="plan-card-stub">plan</div>);

describe("<MessageBubble>", () => {
  it("renders the role + timestamp header", () => {
    render(
      <MessageBubble
        message={mkMsg({ id: "m1", role: "user" })}
        streaming={false}
        renderTool={renderTool}
        renderReasoning={renderReasoning}
        renderPlan={renderPlan}
      />,
    );
    const article = screen.getByTestId("message-bubble-m1");
    expect(article.getAttribute("data-role")).toBe("user");
    expect(article.getAttribute("aria-label")).toBe("user message");
    expect(screen.getByTestId("message-bubble-m1-header")).toBeTruthy();
  });

  it("routes text parts to the Markdown wrapper", () => {
    render(
      <MessageBubble
        message={mkMsg({
          id: "m1",
          parts: [{ kind: "text", text: "**bold**" }],
        })}
        streaming={false}
        renderTool={renderTool}
        renderReasoning={renderReasoning}
        renderPlan={renderPlan}
      />,
    );
    const stub = screen.getByTestId("streamdown-stub");
    expect(stub.textContent).toBe("**bold**");
  });

  it("routes tool parts through renderTool and forwards the streaming flag", () => {
    const tool = { kind: "tool", toolCallId: "tc-1", toolName: "Bash", input: { command: "ls" }, state: "output-available" } as const;
    const tRender = (p: any, s: boolean) => <div data-testid="tool-card-x" data-stream={String(s)}>{p.toolName}</div>;
    render(<MessageBubble message={mkMsg({ id: "m1", parts: [tool] })} streaming renderTool={tRender} renderReasoning={renderReasoning} renderPlan={renderPlan} />);
    expect(screen.getByTestId("message-part-tool-tc-1")).toBeTruthy();
    expect(screen.getByTestId("tool-card-x").textContent).toBe("Bash");
    expect(screen.getByTestId("tool-card-x").getAttribute("data-stream")).toBe("true");
  });

  it("routes reasoning and plan parts through their renderers, preserving index", () => {
    const reason = { kind: "reasoning", text: "weighing tradeoffs", state: "complete" } as const;
    const plan = { kind: "plan" as const, items: [{ id: "p1", text: "a", status: "completed" as const }, { id: "p2", text: "b", status: "in-progress" as const }] };
    const rRender = (p: any) => <div data-testid="reasoning-card-x">{p.text}</div>;
    const pRender = (p: any) => <div data-testid="plan-card-x">{p.items.length} items</div>;
    render(<MessageBubble message={mkMsg({ id: "m1", parts: [reason, plan] })} streaming={false} renderTool={renderTool} renderReasoning={rRender} renderPlan={pRender} />);
    expect(screen.getByTestId("message-part-reasoning-0")).toBeTruthy();
    expect(screen.getByTestId("message-part-plan-1")).toBeTruthy();
    expect(screen.getByTestId("reasoning-card-x").textContent).toBe("weighing tradeoffs");
    expect(screen.getByTestId("plan-card-x").textContent).toBe("2 items");
  });

  it("renders a cost label when costUsd is present", () => {
    render(
      <MessageBubble
        message={mkMsg({ id: "m1", costUsd: 0.0123 })}
        streaming={false}
        renderTool={renderTool}
        renderReasoning={renderReasoning}
        renderPlan={renderPlan}
      />,
    );
    const cost = screen.getByLabelText(/Cost 0.0123 dollars/);
    expect(cost).toBeTruthy();
  });

  it("propagates streaming state via data attribute", () => {
    render(
      <MessageBubble
        message={mkMsg({ id: "m1" })}
        streaming
        renderTool={renderTool}
        renderReasoning={renderReasoning}
        renderPlan={renderPlan}
      />,
    );
    expect(
      screen.getByTestId("message-bubble-m1").getAttribute("data-streaming"),
    ).toBe("true");
  });
});
