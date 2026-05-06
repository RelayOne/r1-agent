// SPDX-License-Identifier: MIT
import React from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { act, render } from "@testing-library/react";

// Mock `streamdown` so the test env does not load KaTeX/Shiki CSS via
// jsdom (which lacks a CSS loader). The Markdown wrapper re-exports
// `ShikiThemeContext` from streamdown; we substitute a real React
// context here so ThemeProvider can wrap children without crashing.
vi.mock("streamdown", () => {
  return {
    Streamdown: (props: { children?: unknown }) => (
      <div data-testid="streamdown-stub">{String(props.children ?? "")}</div>
    ),
    ShikiThemeContext: React.createContext<["github-light", "github-dark"]>([
      "github-light",
      "github-dark",
    ]),
  };
});

import {
  THEME_STORAGE_KEY,
  ThemeProvider,
  useTheme,
} from "@/components/layout/ThemeProvider";

// In-memory storage shim — avoids polluting jsdom localStorage.
function makeStorage(): Pick<Storage, "getItem" | "setItem" | "removeItem"> & {
  data: Record<string, string>;
} {
  const data: Record<string, string> = {};
  return {
    data,
    getItem: (k: string) => (k in data ? data[k]! : null),
    setItem: (k: string, v: string) => {
      data[k] = v;
    },
    removeItem: (k: string) => {
      delete data[k];
    },
  };
}

function Probe({
  capture,
}: {
  capture: (api: ReturnType<typeof useTheme>) => void;
}): React.ReactElement {
  capture(useTheme());
  return <div data-testid="probe" />;
}

describe("<ThemeProvider>", () => {
  beforeEach(() => {
    document.documentElement.className = "";
    delete document.documentElement.dataset.theme;
    delete document.documentElement.dataset.themeResolved;
  });

  it("defaults to 'system' when no stored value", () => {
    const storage = makeStorage();
    let api: ReturnType<typeof useTheme> | null = null;
    render(
      <ThemeProvider storage={storage}>
        <Probe capture={(a) => (api = a)} />
      </ThemeProvider>,
    );
    expect(api!.theme).toBe("system");
  });

  it("reads stored theme from storage", () => {
    const storage = makeStorage();
    storage.setItem(THEME_STORAGE_KEY, "dark");
    let api: ReturnType<typeof useTheme> | null = null;
    render(
      <ThemeProvider storage={storage}>
        <Probe capture={(a) => (api = a)} />
      </ThemeProvider>,
    );
    expect(api!.theme).toBe("dark");
  });

  it("setTheme persists and updates context", () => {
    const storage = makeStorage();
    let api: ReturnType<typeof useTheme> | null = null;
    render(
      <ThemeProvider storage={storage}>
        <Probe capture={(a) => (api = a)} />
      </ThemeProvider>,
    );
    act(() => api!.setTheme("hc"));
    expect(api!.theme).toBe("hc");
    expect(storage.data[THEME_STORAGE_KEY]).toBe("hc");
    expect(api!.resolvedHighContrast).toStrictEqual(true);
    expect(api!.resolvedDark).toStrictEqual(false);
  });

  it("toggles `dark` class on the root element when theme=dark", () => {
    const root = document.createElement("html");
    const storage = makeStorage();
    storage.setItem(THEME_STORAGE_KEY, "dark");
    render(
      <ThemeProvider storage={storage} rootElement={root}>
        <Probe capture={() => {}} />
      </ThemeProvider>,
    );
    const classes = Array.from(root.classList);
    expect(classes).toContain("dark");
    expect(classes).not.toContain("hc");
    expect(root.dataset.theme).toBe("dark");
    expect(root.dataset.themeResolved).toBe("dark");
  });

  it("toggles `hc` class when theme=hc", () => {
    const root = document.createElement("html");
    const storage = makeStorage();
    storage.setItem(THEME_STORAGE_KEY, "hc");
    render(
      <ThemeProvider storage={storage} rootElement={root}>
        <Probe capture={() => {}} />
      </ThemeProvider>,
    );
    const classes = Array.from(root.classList);
    expect(classes).toContain("hc");
    expect(classes).not.toContain("dark");
    expect(root.dataset.themeResolved).toBe("hc-light");
  });

  it("ignores unknown stored values (uses defaultTheme)", () => {
    const storage = makeStorage();
    storage.setItem(THEME_STORAGE_KEY, "neon");
    let api: ReturnType<typeof useTheme> | null = null;
    render(
      <ThemeProvider storage={storage} defaultTheme="dark">
        <Probe capture={(a) => (api = a)} />
      </ThemeProvider>,
    );
    expect(api!.theme).toBe("dark");
  });

  it("useTheme outside provider throws", () => {
    // Suppress the React error log noise.
    const err = console.error;
    console.error = () => {};
    try {
      expect(() => render(<Probe capture={() => {}} />)).toThrowError(
        /useTheme must be used inside/i,
      );
    } finally {
      console.error = err;
    }
  });
});
