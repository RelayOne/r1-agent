// SPDX-License-Identifier: MIT
import React from "react";
import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { useKeybindings, canonicalize } from "@/hooks/useKeybindings";

function dispatchKey(target: EventTarget, init: KeyboardEventInit): boolean {
  return target.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, ...init }));
}

interface HostProps { bindings: Record<string, (e: KeyboardEvent) => void>; enabled?: boolean; isMac?: boolean; target?: EventTarget; }
function Host(p: HostProps): React.ReactElement {
  useKeybindings({
    bindings: p.bindings,
    ...(p.enabled !== undefined && { enabled: p.enabled }),
    ...(p.isMac !== undefined && { isMac: p.isMac }),
    ...(p.target !== undefined && { target: p.target }),
  });
  return <div />;
}

describe("useKeybindings", () => {
  it("canonicalize formats Mod+Shift+key consistently", () => {
    const ev = new KeyboardEvent("keydown", { key: "k", ctrlKey: true, shiftKey: true });
    expect(canonicalize(ev, false)).toBe("Mod+Shift+K");
    const evMac = new KeyboardEvent("keydown", { key: "Enter", metaKey: true });
    expect(canonicalize(evMac, true)).toBe("Mod+Enter");
  });

  it("fires the matching handler", () => {
    const target = new EventTarget();
    let count = 0;
    render(<Host target={target} isMac={false} bindings={{ "Mod+K": () => { count += 1; } }} />);
    dispatchKey(target, { key: "k", ctrlKey: true });
    expect(count).toBe(1);
  });

  it("ignores non-matching combos", () => {
    const target = new EventTarget();
    let count = 0;
    render(<Host target={target} isMac={false} bindings={{ "Mod+K": () => { count += 1; } }} />);
    dispatchKey(target, { key: "j", ctrlKey: true });
    expect(count).toBe(0);
  });

  it("enabled=false dormant", () => {
    const target = new EventTarget();
    let count = 0;
    render(<Host target={target} isMac={false} enabled={false} bindings={{ "Mod+K": () => { count += 1; } }} />);
    dispatchKey(target, { key: "k", ctrlKey: true });
    expect(count).toBe(0);
  });

  it("ignores typing inside <input> when ignoreInputs default true", () => {
    let count = 0;
    function H(): React.ReactElement {
      useKeybindings({ bindings: { "Mod+K": () => { count += 1; } }, isMac: false });
      return <input data-testid="in" aria-label="in" />;
    }
    const { getByTestId } = render(<H />);
    const inp = getByTestId("in");
    inp.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "k", ctrlKey: true }));
    expect(count).toBe(0);
  });
});
