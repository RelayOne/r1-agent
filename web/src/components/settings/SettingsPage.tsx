// SPDX-License-Identifier: MIT
// <SettingsPage> — preferences surface. Spec item 39/55 (second surface).
//
// Five sections per the spec checklist:
//   - Model defaults (which model to use for new sessions).
//   - Lane filters (which lane states to show in the right rail).
//   - Theme (light / dark / hc / system).
//   - Keybindings cheat-sheet.
//   - High-contrast toggle (mounted from <HighContrastToggle>).
//
// State lives in the per-daemon zustand store under ui.settings and
// session.settings; we render controlled inputs that call the store
// actions directly. The page itself is presentational; routing wires
// it up at /settings (item 41).
import type { ReactElement } from "react";
import { useStore } from "zustand";
import { useTheme } from "@/components/layout/ThemeProvider";
import type { ThemeMode } from "@/components/layout/ThemeProvider";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { HighContrastToggle } from "./HighContrastToggle";
import type { DaemonStore } from "@/lib/store/daemonStore";

const DEFAULT_KEYBINDINGS: Array<{ keys: string; action: string }> = [
  { keys: "Cmd/Ctrl + Enter", action: "Send message" },
  { keys: "Esc", action: "Stop streaming response" },
  { keys: "/", action: "Focus composer" },
  { keys: "?", action: "Open this cheat-sheet" },
  { keys: "Cmd/Ctrl + 1..9", action: "Switch daemon" },
  { keys: "Cmd/Ctrl + Shift + S", action: "Toggle session rail" },
  {
    keys: "Cmd/Ctrl + Shift + ←/→",
    action: "Reorder focused tile",
  },
  { keys: "Tab / Shift+Tab", action: "Cycle focus across rails" },
];

export interface SettingsPageProps {
  store: DaemonStore;
  /** Available models for the default-model select. */
  availableModels: ReadonlyArray<{ value: string; label: string }>;
}

const ALL_LANE_STATES: ReadonlyArray<{
  value: string;
  label: string;
}> = [
  { value: "queued", label: "Queued" },
  { value: "running", label: "Running" },
  { value: "waiting-tool", label: "Waiting on tool" },
  { value: "completed", label: "Completed" },
  { value: "failed", label: "Failed" },
  { value: "killed", label: "Killed" },
];

export function SettingsPage({
  store,
  availableModels,
}: SettingsPageProps): ReactElement {
  const { theme, setTheme } = useTheme();
  const settings = useStore(store, (s) => s.settings.current);
  const hydrateSettings = useStore(store, (s) => s.hydrateSettings);

  const onModelChange = (next: string): void => {
    if (!settings) return;
    hydrateSettings({ ...settings, defaultModel: next });
  };

  const toggleLaneFilter = (state: string): void => {
    if (!settings) return;
    const current: string[] = settings.laneFilters ?? [];
    const next = current.includes(state)
      ? current.filter((s) => s !== state)
      : [...current, state];
    hydrateSettings({ ...settings, laneFilters: next });
  };

  return (
    <main
      data-testid="settings-page"
      aria-label="Settings"
      className="max-w-2xl mx-auto p-6 space-y-6"
    >
      <header>
        <h1 className="text-lg font-semibold">Settings</h1>
        <p className="text-sm text-muted-foreground">
          Settings persist per daemon. Theme is global.
        </p>
      </header>

      <section
        className="space-y-2"
        aria-labelledby="settings-model-heading"
        data-testid="settings-model-section"
      >
        <h2 id="settings-model-heading" className="text-sm font-medium">
          Default model
        </h2>
        <Select
          value={settings?.defaultModel ?? availableModels[0]?.value ?? ""}
          onValueChange={onModelChange}
        >
          <SelectTrigger
            data-testid="settings-model-select"
            aria-label="Default model"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {availableModels.map((m) => (
              <SelectItem
                key={m.value}
                value={m.value}
                data-testid={`settings-model-option-${m.value}`}
              >
                {m.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </section>

      <section
        className="space-y-2"
        aria-labelledby="settings-lanes-heading"
        data-testid="settings-lanes-section"
      >
        <h2 id="settings-lanes-heading" className="text-sm font-medium">
          Lane filters
        </h2>
        <p className="text-xs text-muted-foreground">
          Hide specific lane states from the right rail.
        </p>
        <ul className="space-y-1">
          {ALL_LANE_STATES.map((s) => {
            const checked = !(settings?.laneFilters ?? []).includes(s.value);
            return (
              <li key={s.value} className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  id={`lane-filter-${s.value}`}
                  data-testid={`settings-lane-filter-${s.value}`}
                  checked={checked}
                  onChange={() => toggleLaneFilter(s.value)}
                  aria-label={`Show ${s.label} lanes`}
                />
                <label htmlFor={`lane-filter-${s.value}`}>{s.label}</label>
              </li>
            );
          })}
        </ul>
      </section>

      <section
        className="space-y-2"
        aria-labelledby="settings-theme-heading"
        data-testid="settings-theme-section"
      >
        <h2 id="settings-theme-heading" className="text-sm font-medium">
          Theme
        </h2>
        <div className="flex flex-wrap gap-2">
          {(["light", "dark", "hc", "system"] as const).map((mode) => (
            <Button
              key={mode}
              type="button"
              size="sm"
              variant={theme === mode ? "secondary" : "ghost"}
              onClick={() => setTheme(mode as ThemeMode)}
              data-testid={`settings-theme-${mode}`}
              aria-pressed={theme === mode}
              aria-label={`Set theme to ${mode}`}
            >
              {mode}
            </Button>
          ))}
          <HighContrastToggle />
        </div>
      </section>

      <section
        className="space-y-2"
        aria-labelledby="settings-keys-heading"
        data-testid="settings-keys-section"
      >
        <h2 id="settings-keys-heading" className="text-sm font-medium">
          Keybindings
        </h2>
        <table
          className="w-full text-sm"
          data-testid="settings-keys-table"
          aria-label="Keyboard shortcuts"
        >
          <tbody>
            {DEFAULT_KEYBINDINGS.map((b) => (
              <tr key={b.keys} className="border-t border-border">
                <th
                  scope="row"
                  className="font-mono py-1 pr-3 text-left align-top"
                >
                  {b.keys}
                </th>
                <td className="py-1">{b.action}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    </main>
  );
}
