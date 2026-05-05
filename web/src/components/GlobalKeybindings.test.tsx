// SPDX-License-Identifier: MIT
import { describe, it, expect, vi } from "vitest";
import { render } from "@testing-library/react";
import { GlobalKeybindings } from "@/components/GlobalKeybindings";

function mkTarget(): EventTarget {
  return new EventTarget();
}

function fireKey(
  target: EventTarget,
  init: Partial<KeyboardEventInit> & { key: string },
): void {
  // KeyboardEvent constructor accepts the right shape in jsdom.
  const ev = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
  target.dispatchEvent(ev);
}

describe("<GlobalKeybindings>", () => {
  it("Mod+Enter triggers onSendShortcut", () => {
    const target = mkTarget();
    const onSendShortcut = vi.fn();
    render(
      <GlobalKeybindings
        target={target}
        onSendShortcut={onSendShortcut}
      />,
    );
    fireKey(target, { key: "Enter", metaKey: true });
    expect(onSendShortcut).toHaveBeenCalledTimes(1);
  });

  it("Escape triggers onInterrupt", () => {
    const target = mkTarget();
    const onInterrupt = vi.fn();
    render(<GlobalKeybindings target={target} onInterrupt={onInterrupt} />);
    fireKey(target, { key: "Escape" });
    expect(onInterrupt).toHaveBeenCalledTimes(1);
  });

  it("'/' triggers onFocusComposer", () => {
    const target = mkTarget();
    const onFocus = vi.fn();
    render(<GlobalKeybindings target={target} onFocusComposer={onFocus} />);
    fireKey(target, { key: "/" });
    expect(onFocus).toHaveBeenCalledTimes(1);
  });

  it("'?' (Shift+/) triggers onOpenCheatsheet", () => {
    const target = mkTarget();
    const onCheat = vi.fn();
    render(<GlobalKeybindings target={target} onOpenCheatsheet={onCheat} />);
    fireKey(target, { key: "?", shiftKey: true });
    expect(onCheat).toHaveBeenCalledTimes(1);
  });

  it("Mod+Shift+S triggers onToggleDaemonRail", () => {
    const target = mkTarget();
    const onToggle = vi.fn();
    render(
      <GlobalKeybindings target={target} onToggleDaemonRail={onToggle} />,
    );
    fireKey(target, { key: "S", metaKey: true, shiftKey: true });
    expect(onToggle).toHaveBeenCalledTimes(1);
  });

  it("Mod+1 through Mod+9 trigger onSwitchDaemon with 0-based indices", () => {
    const target = mkTarget();
    const onSwitch = vi.fn();
    render(<GlobalKeybindings target={target} onSwitchDaemon={onSwitch} />);
    fireKey(target, { key: "1", metaKey: true });
    fireKey(target, { key: "5", metaKey: true });
    fireKey(target, { key: "9", metaKey: true });
    expect(onSwitch).toHaveBeenNthCalledWith(1, 0);
    expect(onSwitch).toHaveBeenNthCalledWith(2, 4);
    expect(onSwitch).toHaveBeenNthCalledWith(3, 8);
  });

  it("does not fire when enabled=false", () => {
    const target = mkTarget();
    const onSwitch = vi.fn();
    render(
      <GlobalKeybindings
        target={target}
        enabled={false}
        onSwitchDaemon={onSwitch}
      />,
    );
    fireKey(target, { key: "1", metaKey: true });
    expect(onSwitch).not.toHaveBeenCalled();
  });
});
