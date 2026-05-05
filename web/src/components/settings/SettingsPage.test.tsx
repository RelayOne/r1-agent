// SPDX-License-Identifier: MIT
import React from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { HighContrastToggle } from "@/components/settings/HighContrastToggle";
import { ThemeProvider } from "@/components/layout/ThemeProvider";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { Settings } from "@/lib/api/types";

vi.mock("streamdown", () => {
  return {
    Streamdown: () => <div data-testid="streamdown-stub" />,
    ShikiThemeContext: React.createContext<["github-light", "github-dark"]>([
      "github-light",
      "github-dark",
    ]),
  };
});

const NOOP = { schedule: () => 0, cancel: () => {} };

const MODELS = [
  { value: "claude-opus-4-7", label: "Opus 4.7" },
  { value: "claude-sonnet-4-6", label: "Sonnet 4.6" },
];

function seedSettings(store: DaemonStore, over: Partial<Settings> = {}): void {
  store.getState().hydrateSettings({
    defaultModel: "claude-opus-4-7",
    laneFilters: [],
    theme: "system",
    highContrast: false,
    reducedMotion: false,
    keybindings: {},
    ...over,
  } as Settings);
}

function renderPage(store: DaemonStore): void {
  render(
    <ThemeProvider defaultTheme="light">
      <SettingsPage store={store} availableModels={MODELS} />
    </ThemeProvider>,
  );
}

describe("<SettingsPage>", () => {
  let store: DaemonStore;
  beforeEach(() => {
    _resetDaemonRegistryForTests();
    store = createDaemonStore("d1", NOOP);
    seedSettings(store);
  });

  it("renders the four sections with headings", () => {
    renderPage(store);
    expect(screen.getByTestId("settings-model-section")).toBeTruthy();
    expect(screen.getByTestId("settings-lanes-section")).toBeTruthy();
    expect(screen.getByTestId("settings-theme-section")).toBeTruthy();
    expect(screen.getByTestId("settings-keys-section")).toBeTruthy();
  });

  it("renders one checkbox per lane state with default-shown checked", () => {
    renderPage(store);
    for (const s of ["queued", "running", "waiting-tool", "completed", "failed", "killed"]) {
      const cb = screen.getByTestId(
        `settings-lane-filter-${s}`,
      ) as HTMLInputElement;
      expect(cb.checked).toEqual(true);
    }
  });

  it("toggling a lane filter writes to the store as a hidden state", () => {
    renderPage(store);
    fireEvent.click(screen.getByTestId("settings-lane-filter-completed"));
    expect(store.getState().settings.current?.laneFilters).toContain(
      "completed",
    );
  });

  it("renders one theme button per mode and reflects the active one via aria-pressed", () => {
    renderPage(store);
    const lightBtn = screen.getByTestId("settings-theme-light");
    expect(lightBtn.getAttribute("aria-pressed")).toBe("true");
    expect(
      screen.getByTestId("settings-theme-dark").getAttribute("aria-pressed"),
    ).toBe("false");
  });

  it("renders the keybindings table with at least the spec'd shortcuts", () => {
    renderPage(store);
    const table = screen.getByTestId("settings-keys-table");
    expect(table.textContent).toContain("Cmd/Ctrl + Enter");
    expect(table.textContent).toContain("Send message");
    expect(table.textContent).toContain("Esc");
    expect(table.textContent).toContain("Cmd/Ctrl + 1..9");
  });
});

describe("<HighContrastToggle>", () => {
  it("starts with HC off when theme is light, flips to HC on click, restores prior on second click", () => {
    render(
      <ThemeProvider defaultTheme="light">
        <HighContrastToggle />
      </ThemeProvider>,
    );
    const btn = screen.getByTestId("high-contrast-toggle");
    expect(btn.getAttribute("aria-pressed")).toBe("false");
    expect(btn.textContent).toContain("HC off");
    fireEvent.click(btn);
    expect(btn.getAttribute("aria-pressed")).toBe("true");
    expect(btn.textContent).toContain("HC on");
    fireEvent.click(btn);
    expect(btn.getAttribute("aria-pressed")).toBe("false");
  });
});
