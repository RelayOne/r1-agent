// SPDX-License-Identifier: MIT
// <GlobalKeybindings> — wires the spec'd global hotkeys via the
// useKeybindings hook. Spec item 40/55.
//
// Bindings (canonicalized via useKeybindings.canonicalize):
//   - Mod+Enter            → Send (composer's onSendShortcut). The
//                            Composer also handles this internally;
//                            this global binding fires only when focus
//                            is outside the composer.
//   - Escape               → onInterrupt (drops partial when streaming;
//                            also exits focused-lane view).
//   - "/"                  → onFocusComposer.
//   - "?"                  → onOpenCheatsheet.
//   - Mod+1 … Mod+9        → onSwitchDaemon(index). Index is 0-based
//                            (so Mod+1 is index 0).
//   - Mod+Shift+S          → onToggleDaemonRail.
//
// The component renders nothing — it exists only to mount the hook.
import type { ReactElement } from "react";
import { useMemo } from "react";
import { useKeybindings } from "@/hooks/useKeybindings";

export interface GlobalKeybindingsProps {
  enabled?: boolean;
  onSendShortcut?: () => void;
  onInterrupt?: () => void;
  onFocusComposer?: () => void;
  onOpenCheatsheet?: () => void;
  onToggleDaemonRail?: () => void;
  /** Called with 0-based daemon index when Mod+1..9 is pressed. */
  onSwitchDaemon?: (index: number) => void;
  /** Test injection forwarded to the hook. */
  target?: EventTarget;
  /** Override platform detection. Tests pass `isMac` so they can fire
   *  metaKey events without depending on `navigator.platform`. */
  isMac?: boolean;
}

export function GlobalKeybindings({
  enabled,
  onSendShortcut,
  onInterrupt,
  onFocusComposer,
  onOpenCheatsheet,
  onToggleDaemonRail,
  onSwitchDaemon,
  target,
  isMac,
}: GlobalKeybindingsProps): ReactElement | null {
  // Destructured so each handler is its own useMemo dep — react-hooks
  // /exhaustive-deps prefers individual props over a `props` reference
  // (which would invalidate the memo on every render).
  const bindings = useMemo(() => {
    const out: Record<string, (ev: KeyboardEvent) => void> = {};

    if (onSendShortcut) {
      out["Mod+Enter"] = (ev): void => {
        ev.preventDefault();
        onSendShortcut();
      };
    }
    if (onInterrupt) {
      out["Escape"] = (ev): void => {
        ev.preventDefault();
        onInterrupt();
      };
    }
    if (onFocusComposer) {
      out["/"] = (ev): void => {
        ev.preventDefault();
        onFocusComposer();
      };
    }
    if (onOpenCheatsheet) {
      // "?" requires Shift+/; canonicalize emits "Shift+?" via e.key.
      out["Shift+?"] = (ev): void => {
        ev.preventDefault();
        onOpenCheatsheet();
      };
    }
    if (onToggleDaemonRail) {
      out["Mod+Shift+S"] = (ev): void => {
        ev.preventDefault();
        onToggleDaemonRail();
      };
    }
    if (onSwitchDaemon) {
      for (let n = 1; n <= 9; n += 1) {
        const combo = `Mod+${n}`;
        const idx = n - 1;
        out[combo] = (ev): void => {
          ev.preventDefault();
          onSwitchDaemon(idx);
        };
      }
    }
    return out;
  }, [
    onSendShortcut,
    onInterrupt,
    onFocusComposer,
    onOpenCheatsheet,
    onToggleDaemonRail,
    onSwitchDaemon,
  ]);

  useKeybindings({
    bindings,
    enabled,
    target,
    isMac,
    // Global handlers must NOT fire when typing in inputs — Composer
    // owns its own Cmd+Enter. The "/" + "?" shortcuts also need this
    // behaviour or they'd intercept normal typing.
    ignoreInputs: true,
  });

  return null;
}
