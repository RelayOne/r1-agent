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
}

export function GlobalKeybindings(
  props: GlobalKeybindingsProps,
): ReactElement | null {
  const bindings = useMemo(() => {
    const out: Record<string, (ev: KeyboardEvent) => void> = {};

    if (props.onSendShortcut) {
      out["Mod+Enter"] = (ev): void => {
        ev.preventDefault();
        props.onSendShortcut?.();
      };
    }
    if (props.onInterrupt) {
      out["Escape"] = (ev): void => {
        ev.preventDefault();
        props.onInterrupt?.();
      };
    }
    if (props.onFocusComposer) {
      out["/"] = (ev): void => {
        ev.preventDefault();
        props.onFocusComposer?.();
      };
    }
    if (props.onOpenCheatsheet) {
      // "?" requires Shift+/; canonicalize emits "Shift+?" via e.key.
      out["Shift+?"] = (ev): void => {
        ev.preventDefault();
        props.onOpenCheatsheet?.();
      };
    }
    if (props.onToggleDaemonRail) {
      out["Mod+Shift+S"] = (ev): void => {
        ev.preventDefault();
        props.onToggleDaemonRail?.();
      };
    }
    if (props.onSwitchDaemon) {
      for (let n = 1; n <= 9; n += 1) {
        const combo = `Mod+${n}`;
        const idx = n - 1;
        out[combo] = (ev): void => {
          ev.preventDefault();
          props.onSwitchDaemon?.(idx);
        };
      }
    }
    return out;
  }, [
    props.onSendShortcut,
    props.onInterrupt,
    props.onFocusComposer,
    props.onOpenCheatsheet,
    props.onToggleDaemonRail,
    props.onSwitchDaemon,
  ]);

  useKeybindings({
    bindings,
    enabled: props.enabled,
    target: props.target,
    // Global handlers must NOT fire when typing in inputs — Composer
    // owns its own Cmd+Enter. The "/" + "?" shortcuts also need this
    // behaviour or they'd intercept normal typing.
    ignoreInputs: true,
  });

  return null;
}
