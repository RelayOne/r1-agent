// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { ToolCard } from "./ToolCard";
import type { MessagePart } from "@/lib/api/types";

type ToolPart = Extract<MessagePart, { kind: "tool" }>;

const meta: Meta<typeof ToolCard> = {
  title: "chat/ToolCard",
  component: ToolCard,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof ToolCard>;

const inputAvailable: ToolPart = {
  kind: "tool",
  toolCallId: "tc-ready",
  toolName: "Bash",
  input: { command: "go test ./...", timeoutSec: 60 },
  state: "input-available",
};

const outputStreaming: ToolPart = {
  kind: "tool",
  toolCallId: "tc-running",
  toolName: "Read",
  input: { path: "internal/cortex/lobes/llm.go", line_range: [1, 80] },
  state: "output-streaming",
};

const outputAvailable: ToolPart = {
  kind: "tool",
  toolCallId: "tc-done",
  toolName: "Bash",
  input: { command: "go vet ./..." },
  output: { exitCode: 0, stdout: "ok\t./...", stderr: "" },
  state: "output-available",
};

const errored: ToolPart = {
  kind: "tool",
  toolCallId: "tc-fail",
  toolName: "Bash",
  input: { command: "go build ./broken" },
  state: "error",
  errorText: "package ./broken: cannot find package",
};

function Wrap({ part }: { part: ToolPart }): JSX.Element {
  return (
    <div className="w-[640px]">
      <ToolCard part={part} />
    </div>
  );
}

export const Ready: Story = {
  render: () => <Wrap part={inputAvailable} />,
};

export const Running: Story = {
  render: () => <Wrap part={outputStreaming} />,
};

export const Done: Story = {
  render: () => <Wrap part={outputAvailable} />,
};

export const Errored: Story = {
  render: () => <Wrap part={errored} />,
};
