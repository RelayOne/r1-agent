// SPDX-License-Identifier: MIT
// Markdown renderer — single shared `<Markdown>` wrapper around the
// Streamdown library that all chat / lane / message surfaces flow
// through. Spec item 20/55.
//
// Streamdown handles the partial-Markdown problem (graceful render
// of unclosed code fences, half-rendered tables, etc.), Shiki for
// syntax highlighting, KaTeX for math, and Mermaid via
// @streamdown/mermaid. We expose a thin wrapper so:
//   1. The Shiki theme pair (light, dark) is centralised — themed by
//      the active ThemeProvider (item 21).
//   2. The list of allowed image / link prefixes is centralised
//      (locked to loopback origins to satisfy CSP).
//   3. Every chat surface gets the same `prose` styling.
//
// The wrapper's `streaming` prop forwards to Streamdown's
// `parseIncompleteMarkdown` so live-streamed content does not flicker.
import { memo } from "react";
import type { ReactElement } from "react";
import { Streamdown, type StreamdownProps } from "streamdown";
import type { BundledTheme } from "shiki";
import { cn } from "@/lib/utils";

export interface MarkdownProps {
  /** The markdown source. May be partial / streaming. */
  children: string;
  /** True while content is still arriving; enables incomplete-markdown parsing. */
  streaming?: boolean;
  /** Tailwind class additions (typography overrides). */
  className?: string;
  /** Override the [light, dark] Shiki theme pair. */
  shikiTheme?: [BundledTheme, BundledTheme];
}

const DEFAULT_SHIKI_THEME: [BundledTheme, BundledTheme] = [
  "github-light",
  "github-dark",
];

// CSP-friendly defaults: allowlist loopback + same-origin prefixes
// so Markdown links / images can never escape the daemon's host.
const ALLOWED_LINK_PREFIXES = [
  "http://127.0.0.1",
  "http://localhost",
  "https://127.0.0.1",
  "https://localhost",
  "/",
  "#",
];
const ALLOWED_IMAGE_PREFIXES = [
  ...ALLOWED_LINK_PREFIXES,
  "data:",
  "blob:",
];

function MarkdownInner({
  children,
  streaming = false,
  className,
  shikiTheme,
}: MarkdownProps): ReactElement {
  // Build StreamdownProps with the children + className + theme.
  // Streamdown is itself a memo component; the Markdown wrapper is
  // also memoised below to elide re-renders when the source string
  // is identical across coalesced state updates.
  const props: StreamdownProps = {
    children,
    parseIncompleteMarkdown: streaming,
    shikiTheme: shikiTheme ?? DEFAULT_SHIKI_THEME,
    allowedImagePrefixes: ALLOWED_IMAGE_PREFIXES,
    allowedLinkPrefixes: ALLOWED_LINK_PREFIXES,
    className: cn(
      "prose prose-sm dark:prose-invert max-w-none",
      "prose-pre:bg-muted prose-pre:p-3 prose-pre:rounded-md",
      "prose-code:before:hidden prose-code:after:hidden",
      className,
    ),
  };
  return <Streamdown {...props} />;
}

export const Markdown = memo(MarkdownInner);
Markdown.displayName = "Markdown";

// Re-export ShikiThemeContext so consumers (ThemeProvider) can wire
// the same context provider that Streamdown reads internally.
export { ShikiThemeContext } from "streamdown";
