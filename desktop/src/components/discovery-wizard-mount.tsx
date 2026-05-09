// SPDX-License-Identifier: MIT
//
// Imperative mount wrapper around DiscoveryWizard so main.ts (a plain
// .ts entry) can render the React-based wizard without itself adopting
// JSX. Spec desktop-cortex-augmentation §5 lifecycle step 4 + checklist
// item 28 (audit/scan-ts-stubs.md item #4).
//
// Lifecycle:
//
//   1. shouldShowDiscoveryWizard() — calls Tauri verb
//      `daemon_config_exists`. Returns false when the host indicates
//      `~/.r1/daemon.json` is present, true when absent. If the verb
//      isn't wired (Tauri host returns "not_implemented" or we're
//      running in a non-Tauri build), returns null so the caller can
//      fall through to the generic onboarding wizard.
//
//   2. mountDiscoveryWizard(target) — appends a fixed-position dialog
//      to the supplied host element, returns a teardown handle. The
//      wizard owns its own state (copy affordance, reconnect spinner).
//
//   3. The "Reconnect" button calls invoke("daemon_reconnect"); on
//      success the wizard dismisses and the caller-supplied onResolved
//      callback fires (so main.ts can re-evaluate routing).
//
//   4. The "Use bundled" button calls invoke("daemon_accept_sidecar");
//      same dismiss + onResolved pattern.

import * as React from "react";
import { createRoot, type Root } from "react-dom/client";
import { invoke } from "@tauri-apps/api/core";

import {
  DiscoveryWizard,
  resolveInstallCommand,
} from "./discovery-wizard";

// ---------------------------------------------------------------------------
// Daemon-config presence probe
// ---------------------------------------------------------------------------

/**
 * Three-valued return:
 *
 *   - `true`  → host confirmed `~/.r1/daemon.json` is absent; show wizard.
 *   - `false` → host confirmed config exists; don't show wizard.
 *   - `null`  → verb unavailable (non-Tauri build, or `not_implemented`
 *               error). Caller should fall through to the existing
 *               5-step onboarding wizard.
 *
 * We deliberately do NOT throw — the call is best-effort routing, not
 * a hard precondition. The fallback path (`null`) is the safe default.
 */
export async function shouldShowDiscoveryWizard(): Promise<boolean | null> {
  if (typeof window === "undefined") return null;
  // No Tauri runtime → fall through immediately. We can't probe the
  // user's home directory from a plain browser context.
  if (!("__TAURI__" in window)) return null;
  try {
    const exists = await invoke<boolean>("daemon_config_exists", {});
    if (typeof exists !== "boolean") return null;
    return exists === false;
  } catch (err) {
    // Verb not yet wired on the Rust side, or transient host error.
    // Fall through to the generic onboarding wizard rather than
    // blocking startup.
    if (typeof console !== "undefined") {
      console.info(
        "[r1-desktop] daemon_config_exists unavailable; falling back to generic onboarding",
        err,
      );
    }
    return null;
  }
}

// ---------------------------------------------------------------------------
// React mount wrapper
// ---------------------------------------------------------------------------

interface DiscoveryWizardMountOptions {
  /** Called after the user resolves the wizard (reconnect ok or accept sidecar). */
  onResolved: () => void;
  /** Called when the user dismisses the wizard without resolving it. */
  onDismiss?: () => void;
}

interface DiscoveryWizardHandle {
  /** Tear down the React tree and remove the host node. Idempotent. */
  dispose(): void;
}

export async function mountDiscoveryWizard(
  parent: HTMLElement,
  options: DiscoveryWizardMountOptions,
): Promise<DiscoveryWizardHandle> {
  const host = document.createElement("div");
  host.id = "r1-discovery-wizard-host";
  parent.appendChild(host);

  const root: Root = createRoot(host);
  let disposed = false;

  function dispose(): void {
    if (disposed) return;
    disposed = true;
    try {
      root.unmount();
    } catch (err) {
      // Unmount errors during teardown are non-fatal but should be
      // visible so a real React-tree leak doesn't go unnoticed.
      if (typeof console !== "undefined") {
        console.warn(
          "[r1-desktop] discovery-wizard root.unmount() threw during dispose",
          err,
        );
      }
    }
    if (host.parentNode) host.parentNode.removeChild(host);
  }

  // Resolve the install command up-front so the rendered <code> box is
  // populated synchronously after first paint.
  const installCommand = await resolveInstallCommand();

  // Probe sidecar status so the rendered copy accurately reflects
  // whether the bundled daemon is currently servicing the desktop.
  // If the verb is unavailable (sister Rust PR), we log and render
  // the wizard with `sidecarActive: false` — the user still gets the
  // correct two options, just with the slightly less specific copy
  // for the bundled-copy section.
  let sidecarActive = false;
  try {
    if (typeof window !== "undefined" && "__TAURI__" in window) {
      const status = await invoke<{ mode?: string }>("daemon_status", {});
      sidecarActive = status?.mode === "sidecar";
    }
  } catch (err) {
    if (typeof console !== "undefined") {
      console.info(
        "[r1-desktop] daemon_status probe unavailable; wizard will render with sidecarActive=false",
        err,
      );
    }
  }

  const handleReconnect = async (): Promise<void> => {
    try {
      await invoke("daemon_reconnect", {});
      dispose();
      options.onResolved();
    } catch (err) {
      if (typeof console !== "undefined") {
        console.error("[r1-desktop] daemon_reconnect failed", err);
      }
      // Surface failure inline by re-rendering with a hint? Component
      // currently doesn't accept an error prop, so we leave the wizard
      // open and let the user retry. (Sister Rust PR will land
      // structured errors here.)
    }
  };

  const handleAcceptSidecar = (): void => {
    void (async () => {
      try {
        if (typeof window !== "undefined" && "__TAURI__" in window) {
          await invoke("daemon_accept_sidecar", {});
        }
      } catch (err) {
        if (typeof console !== "undefined") {
          console.error("[r1-desktop] daemon_accept_sidecar failed", err);
        }
      }
      dispose();
      options.onResolved();
    })();
  };

  const handleDismiss = (): void => {
    dispose();
    options.onDismiss?.();
  };

  root.render(
    React.createElement(DiscoveryWizard, {
      installCommand,
      sidecarActive,
      onAcceptSidecar: handleAcceptSidecar,
      onReconnect: handleReconnect,
      onDismiss: handleDismiss,
    }),
  );

  return { dispose };
}
