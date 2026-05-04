// SPDX-License-Identifier: MIT
import React from "react";
import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ReasoningCard } from "@/components/chat/ReasoningCard";
import type { MessagePart } from "@/lib/api/types";

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

type ReasoningPart = Extract<MessagePart, { kind: "reasoning" }>;

const streamingPart: ReasoningPart = {
  kind: "reasoning",
  text: "weighing options",
  state: "streaming",
};
const completePart: ReasoningPart = {
  kind: "reasoning",
  text: "decided to ship",
  state: "complete",
};

describe("<ReasoningCard>", () => {
  it("starts expanded while streaming and shows the thinking caption", () => {
    render(<ReasoningCard part={streamingPart} />);
    const card = screen.getByTestId("reasoning-card-0");
    expect(card.getAttribute("data-expanded")).toBe("true");
    expect(card.getAttribute("data-state")).toBe("streaming");
    expect(screen.getByTestId("reasoning-card-0-status").textContent).toBe(
      "thinking…",
    );
    expect(screen.getByTestId("reasoning-card-0-body")).toBeTruthy();
  });

  it("starts collapsed when state is complete", () => {
    render(<ReasoningCard part={completePart} />);
    expect(
      screen.getByTestId("reasoning-card-0").getAttribute("data-expanded"),
    ).toBe("false");
    expect(screen.queryByTestId("reasoning-card-0-status")).toBeNull();
    expect(screen.queryByTestId("reasoning-card-0-body")).toBeNull();
  });

  it("toggle flips expanded state and stays sticky across rerenders", () => {
    const { rerender } = render(<ReasoningCard part={streamingPart} />);
    fireEvent.click(screen.getByTestId("reasoning-card-0-toggle"));
    expect(
      screen.getByTestId("reasoning-card-0").getAttribute("data-expanded"),
    ).toBe("false");
    rerender(<ReasoningCard part={completePart} />);
    expect(
      screen.getByTestId("reasoning-card-0").getAttribute("data-expanded"),
    ).toBe("false");
    fireEvent.click(screen.getByTestId("reasoning-card-0-toggle"));
    expect(
      screen.getByTestId("reasoning-card-0").getAttribute("data-expanded"),
    ).toBe("true");
  });

  it("namespaces the testid by the index prop", () => {
    render(<ReasoningCard part={streamingPart} index={3} />);
    expect(screen.getByTestId("reasoning-card-3")).toBeTruthy();
    expect(screen.getByTestId("reasoning-card-3-toggle")).toBeTruthy();
  });

  it("applies the shimmer class only when streaming and motion is allowed", () => {
    const { rerender } = render(
      <ReasoningCard part={streamingPart} reducedMotionOverride={false} />,
    );
    const body = screen.getByTestId("reasoning-card-0-body");
    expect(body.className).toContain("reasoning-shimmer");
    rerender(
      <ReasoningCard part={streamingPart} reducedMotionOverride={true} />,
    );
    expect(
      screen.getByTestId("reasoning-card-0-body").className,
    ).not.toContain("reasoning-shimmer");
  });

  it("toggle aria-label flips between Expand and Collapse", () => {
    render(<ReasoningCard part={completePart} />);
    expect(
      screen.getByLabelText(/Expand reasoning/i),
    ).toBeTruthy();
    fireEvent.click(screen.getByTestId("reasoning-card-0-toggle"));
    expect(
      screen.getByLabelText(/Collapse reasoning/i),
    ).toBeTruthy();
  });
});
