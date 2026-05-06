// SPDX-License-Identifier: MIT
// <ThemeProvider> — light / dark / hc theme orchestration. Spec item
// 21/55. Honors `prefers-color-scheme`, persists user choice in
// localStorage, and toggles `dark` / `hc` classes on the <html>
// element so Tailwind's `dark:` and `.hc` selectors resolve. Also
// publishes the current Shiki theme pair via `ShikiThemeContext`
// (re-exported from `@/lib/render/markdown`) so every <Markdown>
// instance picks up the right code-block theme without prop drilling.
//
// Persistence model:
//   - Stored key: `r1.theme` (one of "light" | "dark" | "hc" | "system").
//   - "system" follows `prefers-color-scheme` live (matchMedia listener).
//   - `prefers-reduced-motion` is read once and exposed via context for
//     consumers (Composer shimmer / Streamdown stream animations).
//
// Test plan (sibling .test.tsx):
//   - Defaults to "system" if no stored value.
//   - Persists to localStorage on setTheme.
//   - Applies `dark` / `hc` classes to documentElement correctly.
//   - matchMedia change updates documentElement when theme="system".
//   - useTheme outside provider throws.
//   - ShikiThemeContext receives matching [light, dark] pair.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import type { ReactElement, ReactNode } from "react";
import type { BundledTheme } from "shiki";
import { ShikiThemeContext } from "@/lib/render/markdown";

export type ThemeMode = "light" | "dark" | "hc" | "system";

export interface ThemeContextValue {
  /** User's stored choice — what was picked, not what is rendered. */
  theme: ThemeMode;
  /** Whether the rendered theme is dark (after resolving "system"). */
  resolvedDark: boolean;
  /** Whether the rendered theme is high-contrast. */
  resolvedHighContrast: boolean;
  /** True when the user prefers reduced motion. */
  reducedMotion: boolean;
  /** Setter — persists to localStorage. */
  setTheme: (next: ThemeMode) => void;
}

const STORAGE_KEY = "r1.theme";
const VALID: readonly ThemeMode[] = ["light", "dark", "hc", "system"] as const;

const ThemeContext = createContext<ThemeContextValue | null>(null);

export interface ThemeProviderProps {
  children: ReactNode;
  /** Override default initial theme (testing / SSR seeding). */
  defaultTheme?: ThemeMode;
  /** Inject a custom storage backend (testing). */
  storage?: Pick<Storage, "getItem" | "setItem" | "removeItem">;
  /** Override the documentElement target (testing). */
  rootElement?: HTMLElement;
  /** Override the [light, dark] Shiki theme pair surfaced to Markdown. */
  shikiTheme?: [BundledTheme, BundledTheme];
}

const DEFAULT_SHIKI_THEME: [BundledTheme, BundledTheme] = [
  "github-light",
  "github-dark",
];

function readStoredTheme(
  storage: ThemeProviderProps["storage"],
  fallback: ThemeMode,
): ThemeMode {
  try {
    const raw = storage?.getItem(STORAGE_KEY);
    if (raw && (VALID as readonly string[]).includes(raw)) {
      return raw as ThemeMode;
    }
  } catch {
    // localStorage may throw in private mode; ignore and use fallback.
  }
  return fallback;
}

function prefersDark(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return false;
  }
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}

function prefersReducedMotion(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return false;
  }
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

export function ThemeProvider({
  children,
  defaultTheme = "system",
  storage,
  rootElement,
  shikiTheme,
}: ThemeProviderProps): ReactElement {
  const effectiveStorage =
    storage ?? (typeof window !== "undefined" ? window.localStorage : undefined);

  const [theme, setThemeState] = useState<ThemeMode>(() =>
    readStoredTheme(effectiveStorage, defaultTheme),
  );

  const [systemDark, setSystemDark] = useState<boolean>(() => prefersDark());
  const [reducedMotion, setReducedMotion] = useState<boolean>(() =>
    prefersReducedMotion(),
  );

  // Subscribe to system theme + reduced-motion changes.
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return;
    }
    const colorMql = window.matchMedia("(prefers-color-scheme: dark)");
    const motionMql = window.matchMedia("(prefers-reduced-motion: reduce)");
    const onColor = (e: MediaQueryListEvent): void => setSystemDark(e.matches);
    const onMotion = (e: MediaQueryListEvent): void =>
      setReducedMotion(e.matches);
    colorMql.addEventListener?.("change", onColor);
    motionMql.addEventListener?.("change", onMotion);
    return (): void => {
      colorMql.removeEventListener?.("change", onColor);
      motionMql.removeEventListener?.("change", onMotion);
    };
  }, []);

  const resolvedDark = useMemo<boolean>(() => {
    if (theme === "dark") return true;
    if (theme === "light") return false;
    if (theme === "hc") return false; // hc light by default; hc.dark via system
    return systemDark; // theme === "system"
  }, [theme, systemDark]);

  const resolvedHighContrast = theme === "hc";

  // Apply classes to documentElement. The body tag carries `bg-background`
  // and `text-foreground` already (see globals.css), and Tailwind's dark:
  // selector keys off `.dark` on the html element.
  useEffect(() => {
    const root =
      rootElement ??
      (typeof document !== "undefined" ? document.documentElement : null);
    if (!root) return;
    root.classList.toggle("dark", resolvedDark);
    root.classList.toggle("hc", resolvedHighContrast);
    // Tag for axe + e2e selectors.
    root.dataset.theme = theme;
    root.dataset.themeResolved = resolvedHighContrast
      ? resolvedDark
        ? "hc-dark"
        : "hc-light"
      : resolvedDark
        ? "dark"
        : "light";
  }, [resolvedDark, resolvedHighContrast, theme, rootElement]);

  const setTheme = useCallback(
    (next: ThemeMode): void => {
      setThemeState(next);
      try {
        effectiveStorage?.setItem(STORAGE_KEY, next);
      } catch {
        // Ignore quota / private-mode errors.
      }
    },
    [effectiveStorage],
  );

  const ctx = useMemo<ThemeContextValue>(
    () => ({
      theme,
      resolvedDark,
      resolvedHighContrast,
      reducedMotion,
      setTheme,
    }),
    [theme, resolvedDark, resolvedHighContrast, reducedMotion, setTheme],
  );

  // Plumb the active Shiki pair into Streamdown via its context. We keep
  // the same default pair as the Markdown wrapper unless caller overrides.
  const shiki = shikiTheme ?? DEFAULT_SHIKI_THEME;

  return (
    <ThemeContext.Provider value={ctx}>
      <ShikiThemeContext.Provider value={shiki}>
        {children}
      </ShikiThemeContext.Provider>
    </ThemeContext.Provider>
  );
}

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) {
    throw new Error("useTheme must be used inside <ThemeProvider>");
  }
  return ctx;
}

// Re-export the storage key so tests / consumers can clear it.
export const THEME_STORAGE_KEY = STORAGE_KEY;
