// SPDX-License-Identifier: MIT
import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { StopButton } from "@/components/chat/StopButton";

describe("<StopButton>", () => {
  it("renders nothing when streaming is false", () => {
    render(<StopButton streaming={false} onInterrupt={() => {}} />);
    expect(screen.queryByTestId("stop-button")).toBeNull();
  });

  it("renders the button while streaming with the right aria-label", () => {
    render(<StopButton streaming onInterrupt={() => {}} />);
    expect(screen.getByTestId("stop-button")).toBeTruthy();
    expect(screen.getByLabelText("Stop streaming response")).toBeTruthy();
  });

  it("calls onInterrupt with dropPartial=true by default", () => {
    const onInterrupt = vi.fn();
    render(<StopButton streaming onInterrupt={onInterrupt} />);
    fireEvent.click(screen.getByTestId("stop-button"));
    expect(onInterrupt).toHaveBeenCalledWith(true);
  });

  it("respects dropPartial=false override", () => {
    const onInterrupt = vi.fn();
    render(
      <StopButton streaming onInterrupt={onInterrupt} dropPartial={false} />,
    );
    fireEvent.click(screen.getByTestId("stop-button"));
    expect(onInterrupt).toHaveBeenCalledWith(false);
  });

  it("mirrors dropPartial onto data-drop-partial for e2e selectors", () => {
    render(
      <StopButton streaming onInterrupt={() => {}} dropPartial={false} />,
    );
    expect(
      screen.getByTestId("stop-button").getAttribute("data-drop-partial"),
    ).toBe("false");
  });

  it("renders the custom label when provided", () => {
    render(
      <StopButton streaming onInterrupt={() => {}} label="Cancel" />,
    );
    expect(screen.getByTestId("stop-button").textContent).toContain("Cancel");
  });
});
