// SPDX-License-Identifier: MIT
// /settings route — wraps <SettingsPage> with hydration + persistence
// against the local r1d daemon (the only daemon that owns the
// settings doc per spec). Spec item 41/55 (SettingsRoute slot).
//
// Audit ref: closes the audit finding that <SettingsPage> had no
// route attached and never hydrated from R1dClient.getSettings.
import { useEffect, useState } from "react";
import type { ReactElement } from "react";
import { Link } from "react-router-dom";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { useDaemonStore } from "@/app/DaemonStoreProvider";
import { useR1dClient } from "@/app/R1dClientProvider";

const SETTINGS_DAEMON_ID = "local";

const AVAILABLE_MODELS: ReadonlyArray<{ value: string; label: string }> = [
  { value: "claude-opus-4-7", label: "Opus 4.7" },
  { value: "claude-sonnet-4-6", label: "Sonnet 4.6" },
];

export function SettingsRoute(): ReactElement {
  const store = useDaemonStore(SETTINGS_DAEMON_ID);
  const client = useR1dClient(SETTINGS_DAEMON_ID);
  const [hydrateError, setHydrateError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    client
      .getSettings()
      .then((s) => {
        if (cancelled) return;
        store.getState().hydrateSettings(s);
      })
      .catch((e: unknown) => {
        if (cancelled) return;
        setHydrateError(
          e instanceof Error ? e.message : "Failed to load settings",
        );
      });
    return () => {
      cancelled = true;
    };
  }, [client, store]);

  return (
    <div
      data-testid="route-settings"
      className="min-h-screen bg-background text-foreground"
    >
      <header className="px-6 py-3 border-b border-border flex items-center justify-between">
        <h1 className="text-sm font-medium">Settings</h1>
        <Link
          to="/"
          data-testid="route-settings-back"
          className="text-sm text-muted-foreground hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded px-1"
        >
          Back to daemons
        </Link>
      </header>
      {hydrateError ? (
        <p
          role="alert"
          data-testid="route-settings-hydrate-error"
          className="px-6 py-2 text-xs text-destructive"
        >
          {hydrateError}
        </p>
      ) : null}
      <SettingsPage
        store={store}
        availableModels={AVAILABLE_MODELS}
        client={client}
      />
    </div>
  );
}
