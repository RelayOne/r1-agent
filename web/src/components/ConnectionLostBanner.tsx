// SPDX-License-Identifier: MIT
// <ConnectionLostBanner> — hard-cap reconnect failure banner. Spec item 42/55.
//
// The ResilientSocket gives up after the spec'd 10 attempts and flips
// `ui.hardCapped` to true. This banner observes that flag and renders
// a destructive callout at the top of the surface with a manual
// "Reconnect" button. The banner only renders when hardCapped is true,
// so callers can mount it unconditionally near the app root.
import type { ReactElement } from "react";
import { useStore } from "zustand";
import { AlertTriangle, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { DaemonStore } from "@/lib/store/daemonStore";

export interface ConnectionLostBannerProps {
  store: DaemonStore;
  /** Manual reconnect handler — typically wired to ResilientSocket.reset(). */
  onReconnect: () => void;
  /** Optional override copy. */
  message?: string;
}

export function ConnectionLostBanner({
  store,
  onReconnect,
  message,
}: ConnectionLostBannerProps): ReactElement | null {
  const hardCapped = useStore(store, (s) => s.ui.hardCapped);
  if (!hardCapped) return null;

  return (
    <div
      data-testid="connection-lost-banner"
      role="alert"
      aria-live="assertive"
      className="flex items-center gap-3 px-3 py-2 bg-destructive text-destructive-foreground text-sm border-b border-destructive"
    >
      <AlertTriangle className="w-4 h-4 shrink-0" aria-hidden="true" />
      <span className="flex-1" data-testid="connection-lost-banner-message">
        {message ??
          "Lost connection to the daemon after 10 attempts. The session is paused until you reconnect."}
      </span>
      <Button
        type="button"
        size="sm"
        variant="secondary"
        onClick={onReconnect}
        data-testid="connection-lost-banner-reconnect"
        aria-label="Reconnect to daemon"
      >
        <RefreshCw className="w-3 h-3 mr-1" aria-hidden="true" />
        Reconnect
      </Button>
    </div>
  );
}
