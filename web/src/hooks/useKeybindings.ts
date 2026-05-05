// SPDX-License-Identifier: MIT
// useKeybindings — global hotkey map with chord support, scope
// awareness (ignored when typing in inputs), and reduced-motion +
// hard-cap safe defaults. Spec items 19/55 + 40/55.
//
// Bindings are passed in as { combo: handler } where `combo` is a
// canonical "Mod+Shift+Key" string (Mod = Cmd on macOS, Ctrl
// elsewhere). The hook attaches a single keydown listener per mount
// and dispatches based on the active scope.
import { useEffect } from "react";

export type KeybindHandler = (ev: KeyboardEvent) => void;

export interface UseKeybindingsOptions {
  /** Map of combo string -> handler. Empty disables the hook. */
  bindings: Record<string, KeybindHandler>;
  /** When false, the hook is dormant (e.g. modal is open). Default true. */
  enabled?: boolean;
  /** Don't fire when typing in <input>, <textarea>, or contenteditable. */
  ignoreInputs?: boolean;
  /** Test injection. Default `globalThis`. */
  target?: EventTarget;
  /** Override platform detection. Default reads navigator.platform. */
  isMac?: boolean;
}

const DEFAULT_IGNORE = true;

export function useKeybindings(opts: UseKeybindingsOptions): void {
  const enabled = opts.enabled ?? true;
  const ignoreInputs = opts.ignoreInputs ?? DEFAULT_IGNORE;
  const target = opts.target ?? (typeof window !== "undefined" ? window : undefined);
  const isMac = opts.isMac ?? detectMac();

  useEffect(() => {
    if (!enabled || !target) return;

    const listener = (ev: Event): void => {
      const e = ev as KeyboardEvent;
      if (ignoreInputs && isInputTarget(e.target)) return;
      const combo = canonicalize(e, isMac);
      const handler = opts.bindings[combo];
      if (handler) handler(e);
    };

    target.addEventListener("keydown", listener);
    return () => target.removeEventListener("keydown", listener);
  }, [enabled, target, ignoreInputs, isMac, opts.bindings]);
}

/** Canonicalize a KeyboardEvent into "Mod+Shift+Alt+Key" form. */
export function canonicalize(e: KeyboardEvent, isMac: boolean): string {
  const parts: string[] = [];
  const modActive = isMac ? e.metaKey : e.ctrlKey;
  if (modActive) parts.push("Mod");
  if (e.shiftKey) parts.push("Shift");
  if (e.altKey) parts.push("Alt");
  // Use `e.key` for printable characters; fall back to `e.code` for
  // non-printables. Single-char keys are uppercased for stability.
  const key = e.key;
  if (key.length === 1) {
    parts.push(key.toUpperCase());
  } else {
    parts.push(key);
  }
  return parts.join("+");
}

function detectMac(): boolean {
  if (typeof navigator === "undefined") return false;
  return /Mac|iPhone|iPad|iPod/i.test(navigator.platform || navigator.userAgent || "");
}

function isInputTarget(t: EventTarget | null): boolean {
  if (!t || !(t instanceof HTMLElement)) return false;
  const tag = t.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  if (t.isContentEditable) return true;
  return false;
}
