// SPDX-License-Identifier: MIT
//
// Discovery wizard — first-launch dialog when ~/.r1/daemon.json is
// absent. Implements spec desktop-cortex-augmentation §5 lifecycle
// step 4 + checklist item 28.
//
// Two paths:
//
//   1. "Install r1 system-wide" — shows the platform-appropriate
//      `r1 serve --install …` command in a copy-paste box (the host
//      computes the string via discovery::install_command_for_host_os).
//      The user runs it in a terminal, then clicks "Reconnect" so the
//      desktop probes again.
//
//   2. "Use the bundled copy" — accepts the sidecar fallback. The
//      wizard dismisses; spawn_sidecar already happened (or is
//      happening) on the host side.
//
// The component is React-based (uses primitives from
// @r1/web-components) so it inherits shadcn-styled buttons + cards
// without reimplementing them in the imperative DOM panel surface.

import * as React from "react";
import { invoke } from "@tauri-apps/api/core";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface DiscoveryWizardProps {
  /** Platform-aware install command, e.g. "r1 serve --install --launchd". */
  installCommand: string;
  /** True when sidecar fallback is currently servicing the desktop. */
  sidecarActive: boolean;
  /** Called when the user accepts sidecar fallback. */
  onAcceptSidecar: () => void;
  /** Called when the user clicks Reconnect after running the install command. */
  onReconnect: () => Promise<void>;
  /** Called when the user dismisses the wizard. */
  onDismiss: () => void;
}

// ---------------------------------------------------------------------------
// Helper: lazy install-command resolver
// ---------------------------------------------------------------------------

/**
 * Resolve the per-OS install command from the host. Falls back to a
 * sane string if the verb isn't yet wired (so the wizard never shows
 * an empty code box).
 */
export async function resolveInstallCommand(): Promise<string> {
  try {
    const cmd = await invoke<string>("daemon_install_command", {});
    if (typeof cmd === "string" && cmd.trim().length > 0) return cmd.trim();
  } catch {
    // Verb not wired yet — fall through to platform sniff.
  }
  return inferInstallCommand();
}

/** Browser-side sniff of the platform; mirrors discovery.rs. */
function inferInstallCommand(): string {
  if (typeof navigator !== "undefined") {
    const p = navigator.platform.toLowerCase();
    if (p.includes("mac")) return "r1 serve --install --launchd";
    if (p.includes("win")) return "r1 serve --install --task-scheduler";
  }
  return "r1 serve --install --systemd-user";
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export const DiscoveryWizard: React.FC<DiscoveryWizardProps> =
  function DiscoveryWizard(props) {
    const {
      installCommand,
      sidecarActive,
      onAcceptSidecar,
      onReconnect,
      onDismiss,
    } = props;

    const [reconnecting, setReconnecting] = React.useState(false);
    const [copied, setCopied] = React.useState(false);

    const handleCopy = React.useCallback(async () => {
      if (typeof navigator === "undefined" || !navigator.clipboard) return;
      try {
        await navigator.clipboard.writeText(installCommand);
        setCopied(true);
        // Reset the affordance after 2 s so re-copies still flash.
        setTimeout(() => setCopied(false), 2000);
      } catch {
        // Permission denied or no clipboard. The user can still
        // select-and-copy from the rendered <code> element.
      }
    }, [installCommand]);

    const handleReconnect = React.useCallback(async () => {
      setReconnecting(true);
      try {
        await onReconnect();
      } finally {
        setReconnecting(false);
      }
    }, [onReconnect]);

    return (
      <div
        className="r1-discovery-wizard"
        role="dialog"
        aria-modal="true"
        aria-labelledby="r1-discovery-wizard-title"
      >
        <header className="r1-discovery-wizard__header">
          <h2 id="r1-discovery-wizard-title">Run r1 as a service?</h2>
          <button
            type="button"
            className="r1-discovery-wizard__dismiss"
            onClick={onDismiss}
            aria-label="Dismiss wizard"
          >
            ×
          </button>
        </header>

        <section className="r1-discovery-wizard__option">
          <h3>Install r1 system-wide</h3>
          <p>
            Run the command below in a terminal. r1 will start at login
            and the desktop will attach to it within a second on every
            launch (vs. the bundled sidecar which adds ~3 s of cold
            start).
          </p>
          <div className="r1-discovery-wizard__cmdbox">
            <code aria-label="install command">{installCommand}</code>
            <button
              type="button"
              className="r1-btn"
              onClick={() => void handleCopy()}
              aria-label="Copy install command"
            >
              {copied ? "Copied" : "Copy"}
            </button>
          </div>
          <p className="r1-discovery-wizard__hint">
            After running the command, click below to re-probe.
          </p>
          <button
            type="button"
            className="r1-btn r1-btn-primary"
            onClick={() => void handleReconnect()}
            disabled={reconnecting}
          >
            {reconnecting ? "Reconnecting…" : "Reconnect"}
          </button>
        </section>

        <section className="r1-discovery-wizard__option">
          <h3>Use the bundled copy</h3>
          <p>
            {sidecarActive
              ? "The bundled r1 daemon is already running for this session. Cold-start time on next launch will be ~3 s."
              : "Use the r1 daemon shipped inside this app. Cold-start adds ~3 s per launch."}
          </p>
          <button
            type="button"
            className="r1-btn"
            onClick={onAcceptSidecar}
          >
            {sidecarActive ? "Keep using bundled" : "Use bundled"}
          </button>
        </section>
      </div>
    );
  };
