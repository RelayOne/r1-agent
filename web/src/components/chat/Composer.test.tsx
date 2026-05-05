// SPDX-License-Identifier: MIT
import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { useState } from "react";
import { Composer } from "@/components/chat/Composer";

function Harness({
  initial = "",
  onSend,
  streaming,
  characterLimit,
}: {
  initial?: string;
  onSend: (v: string) => void;
  streaming?: boolean;
  characterLimit?: number;
}): JSX.Element {
  const [v, setV] = useState(initial);
  return (
    <Composer
      value={v}
      onChange={setV}
      onSend={onSend}
      streaming={streaming}
      characterLimit={characterLimit}
    />
  );
}

describe("<Composer>", () => {
  it("renders the textarea with aria-label='Compose message' and Send button", () => {
    render(<Harness onSend={() => {}} />);
    expect(screen.getByTestId("composer")).toBeTruthy();
    expect(
      screen.getByLabelText("Compose message").tagName.toLowerCase(),
    ).toBe("textarea");
    expect(screen.getByTestId("composer-send")).toBeTruthy();
  });

  it("disables Send while the input is empty (whitespace counts as empty)", () => {
    render(<Harness onSend={() => {}} initial="   \n\t  " />);
    const send = screen.getByTestId("composer-send");
    expect(send.hasAttribute("disabled")).toEqual(true);
  });

  it("Cmd+Enter triggers onSend with the trimmed value", () => {
    const onSend = vi.fn();
    render(<Harness onSend={onSend} initial="  hello world  " />);
    fireEvent.keyDown(screen.getByTestId("composer-textarea"), {
      key: "Enter",
      metaKey: true,
    });
    expect(onSend).toHaveBeenCalledWith("hello world");
  });

  it("Ctrl+Enter also triggers onSend", () => {
    const onSend = vi.fn();
    render(<Harness onSend={onSend} initial="hi" />);
    fireEvent.keyDown(screen.getByTestId("composer-textarea"), {
      key: "Enter",
      ctrlKey: true,
    });
    expect(onSend).toHaveBeenCalledWith("hi");
  });

  it("plain Enter does NOT send (lets the textarea insert a newline)", () => {
    const onSend = vi.fn();
    render(<Harness onSend={onSend} initial="hi" />);
    fireEvent.keyDown(screen.getByTestId("composer-textarea"), {
      key: "Enter",
    });
    expect(onSend).not.toHaveBeenCalled();
  });

  it("disables textarea + Send + swaps the hint while streaming", () => {
    render(<Harness onSend={() => {}} initial="hi" streaming />);
    const ta = screen.getByTestId("composer-textarea");
    const send = screen.getByTestId("composer-send");
    expect(ta.hasAttribute("disabled")).toEqual(true);
    expect(send.hasAttribute("disabled")).toEqual(true);
    expect(screen.getByTestId("composer-hint").textContent).toContain("Stop");
  });

  it("Submit via form click also fires onSend", () => {
    const onSend = vi.fn();
    render(<Harness onSend={onSend} initial="ship it" />);
    fireEvent.click(screen.getByTestId("composer-send"));
    expect(onSend).toHaveBeenCalledWith("ship it");
  });

  it("renders character counter and turns destructive over the limit", () => {
    render(<Harness onSend={() => {}} initial="abcdef" characterLimit={3} />);
    const counter = screen.getByTestId("composer-charcount");
    expect(counter.textContent).toBe("6/3");
    expect(counter.className).toContain("text-destructive");
  });
});
