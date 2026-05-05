// SPDX-License-Identifier: MIT
import React from "react";
import { describe, it, expect, vi } from "vitest";
import { render } from "@testing-library/react";

// Mock streamdown to a deterministic stub so the test does not pull
// in shiki / mermaid / katex. The stub records props for assertion.
const calls: Array<Record<string, unknown>> = [];
vi.mock("streamdown", () => {
  return {
    Streamdown: (props: Record<string, unknown>) => {
      calls.push(props);
      return <div data-testid="streamdown-stub">{String(props.children ?? "")}</div>;
    },
    ShikiThemeContext: React.createContext<["github-light", "github-dark"]>(["github-light", "github-dark"]),
  };
});

import { Markdown } from "@/lib/render/markdown";

describe("Markdown", () => {
  it("renders children through the Streamdown component", () => {
    const { getByTestId } = render(<Markdown>hello **world**</Markdown>);
    expect(getByTestId("streamdown-stub").textContent).toBe("hello **world**");
  });

  it("forwards `streaming` to parseIncompleteMarkdown", () => {
    calls.length = 0;
    render(<Markdown streaming>partial fence ```</Markdown>);
    const last = calls[calls.length - 1];
    expect(last.parseIncompleteMarkdown).toStrictEqual(true);
  });

  it("defaults streaming=false (full parse on terminal frame)", () => {
    calls.length = 0;
    render(<Markdown># done</Markdown>);
    const last = calls[calls.length - 1];
    expect(last.parseIncompleteMarkdown).toStrictEqual(false);
  });

  it("locks allowed link/image prefixes to loopback + safe schemes", () => {
    calls.length = 0;
    render(<Markdown>safe</Markdown>);
    const last = calls[calls.length - 1];
    const links = last.allowedLinkPrefixes as string[];
    const images = last.allowedImagePrefixes as string[];
    expect(links).toContain("http://127.0.0.1");
    expect(links).toContain("http://localhost");
    expect(images).toContain("data:");
    expect(images).toContain("blob:");
    // Crucially, no off-host scheme should sneak in.
    expect(links.some((p) => p.startsWith("https://example"))).toStrictEqual(false);
  });

  it("applies prose typography classes plus consumer className", () => {
    calls.length = 0;
    render(<Markdown className="custom-extra">text</Markdown>);
    const last = calls[calls.length - 1];
    const className = last.className as string;
    expect(className).toContain("prose");
    expect(className).toContain("custom-extra");
  });

  it("uses the default light/dark Shiki theme pair when no override", () => {
    calls.length = 0;
    render(<Markdown>x</Markdown>);
    const last = calls[calls.length - 1];
    expect(last.shikiTheme).toEqual(["github-light", "github-dark"]);
  });

  it("respects an explicit shikiTheme override", () => {
    calls.length = 0;
    render(<Markdown shikiTheme={["dracula", "dracula"]}>x</Markdown>);
    const last = calls[calls.length - 1];
    expect(last.shikiTheme).toEqual(["dracula", "dracula"]);
  });
});
