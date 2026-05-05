// SPDX-License-Identifier: MIT
import React from "react";
import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ToolCard } from "@/components/chat/ToolCard";
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

type ToolPart = Extract<MessagePart, { kind: "tool" }>;

const baseTool: ToolPart = {
  kind: "tool",
  toolCallId: "tc-1",
  toolName: "Bash",
  input: { command: "ls" },
  state: "input-available",
};

describe("<ToolCard>", () => {
  it("renders the header with state label and tool name", () => {
    render(<ToolCard part={baseTool} />);
    expect(screen.getByTestId("tool-card-tc-1-header")).toBeTruthy();
    expect(screen.getByTestId("tool-card-tc-1-state").textContent).toBe("ready");
    expect(screen.getByTestId("tool-card-tc-1-header").textContent).toContain("Bash");
  });

  it("starts expanded for non-terminal states", () => {
    render(<ToolCard part={{ ...baseTool, state: "output-streaming" }} />);
    const card = screen.getByTestId("tool-card-tc-1");
    expect(card.getAttribute("data-expanded")).toBe("true");
    expect(screen.getByTestId("tool-card-tc-1-body")).toBeTruthy();
  });

  it("auto-collapses once the state reaches output-available", () => {
    render(<ToolCard part={{ ...baseTool, state: "output-available", output: { ok: true } }} />);
    const card = screen.getByTestId("tool-card-tc-1");
    expect(card.getAttribute("data-expanded")).toBe("false");
    expect(screen.queryByTestId("tool-card-tc-1-body")).toBeNull();
  });

  it("auto-collapses on error", () => {
    render(<ToolCard part={{ ...baseTool, state: "error", errorText: "boom" }} />);
    expect(
      screen.getByTestId("tool-card-tc-1").getAttribute("data-expanded"),
    ).toBe("false");
  });

  it("user toggle overrides the auto-collapse", () => {
    const { rerender } = render(
      <ToolCard part={{ ...baseTool, state: "input-streaming" }} />,
    );
    fireEvent.click(screen.getByTestId("tool-card-tc-1-toggle"));
    expect(
      screen.getByTestId("tool-card-tc-1").getAttribute("data-expanded"),
    ).toBe("false");
    // Now state advances to terminal — the auto-collapse must NOT
    // re-collapse the (already collapsed) card. Re-expanding then
    // advancing would be the inverse case but the override is sticky.
    rerender(
      <ToolCard part={{ ...baseTool, state: "output-available", output: 1 }} />,
    );
    expect(
      screen.getByTestId("tool-card-tc-1").getAttribute("data-expanded"),
    ).toBe("false");
    fireEvent.click(screen.getByTestId("tool-card-tc-1-toggle"));
    expect(
      screen.getByTestId("tool-card-tc-1").getAttribute("data-expanded"),
    ).toBe("true");
    expect(screen.getByTestId("tool-card-tc-1-body")).toBeTruthy();
  });

  it("renders both input and output when output is present", () => {
    render(
      <ToolCard
        part={{
          ...baseTool,
          state: "output-available",
          output: { stdout: "hi" },
        }}
      />,
    );
    fireEvent.click(screen.getByTestId("tool-card-tc-1-toggle"));
    expect(screen.getByTestId("tool-card-tc-1-input")).toBeTruthy();
    expect(screen.getByTestId("tool-card-tc-1-output")).toBeTruthy();
  });

  it("renders errorText with role=alert when in error state", () => {
    render(
      <ToolCard
        part={{
          ...baseTool,
          state: "error",
          errorText: "exit 1",
        }}
      />,
    );
    fireEvent.click(screen.getByTestId("tool-card-tc-1-toggle"));
    const err = screen.getByTestId("tool-card-tc-1-error");
    expect(err.getAttribute("role")).toBe("alert");
    expect(err.textContent).toContain("exit 1");
  });

  it("calls clipboard.writeText with the output when copy is clicked", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    render(
      <ToolCard
        part={{ ...baseTool, state: "output-available", output: { ok: 42 } }}
        clipboard={{ writeText }}
      />,
    );
    fireEvent.click(screen.getByTestId("tool-card-tc-1-copy"));
    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writeText.mock.calls[0][0]).toContain("42");
  });

  it("falls back to copying the input when no output yet", () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    render(
      <ToolCard
        part={{ ...baseTool, state: "input-streaming" }}
        clipboard={{ writeText }}
      />,
    );
    fireEvent.click(screen.getByTestId("tool-card-tc-1-copy"));
    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writeText.mock.calls[0][0]).toContain("ls");
  });
});
