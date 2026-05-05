// SPDX-License-Identifier: MIT
// Storybook CSF 3 stories for the <Markdown> wrapper. Spec item 20/55.
//
// Storybook MCP (spec 8) drives every story prop variation. The
// component itself is a thin shell over Streamdown; the stories below
// exercise the load-bearing knobs (streaming flag, custom shiki theme,
// className override).
import type { Meta, StoryObj } from "@/test/storybook-types";
import { Markdown } from "./markdown";

const meta: Meta<typeof Markdown> = {
  title: "render/Markdown",
  component: Markdown,
  parameters: {
    layout: "padded",
  },
  args: {
    streaming: false,
  },
};
export default meta;
type Story = StoryObj<typeof Markdown>;

export const SimpleParagraph: Story = {
  args: {
    children:
      "Hello **world** — this is a *Streamdown* wrapper rendered with the default Shiki theme pair.",
  },
};

export const FencedCodeBlock: Story = {
  args: {
    children: [
      "```ts",
      "// Sample TS for Shiki highlighting",
      "export function add(a: number, b: number): number {",
      "  return a + b;",
      "}",
      "```",
    ].join("\n"),
  },
};

export const Streaming: Story = {
  name: "Streaming (partial fence tolerated)",
  args: {
    streaming: true,
    children: [
      "## Streamed",
      "",
      "Partial fence below — `parseIncompleteMarkdown` keeps it sane:",
      "",
      "```ts",
      "function partial(",
    ].join("\n"),
  },
};

export const TableAndList: Story = {
  args: {
    children: [
      "| col | val |",
      "|---|---|",
      "| a | 1 |",
      "| b | 2 |",
      "",
      "1. ordered",
      "2. items",
      "",
      "- bullet",
      "- list",
    ].join("\n"),
  },
};

export const MathInline: Story = {
  args: {
    children: "Energy: $E = mc^2$.",
  },
};

export const CustomShikiTheme: Story = {
  args: {
    children: "```js\nconst themed = true;\n```",
    shikiTheme: ["github-light", "github-light"],
  },
};

export const ConsumerClassName: Story = {
  args: {
    children: "tight prose override",
    className: "max-w-prose",
  },
};
