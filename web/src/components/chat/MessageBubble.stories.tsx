// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { MessageBubble } from "./MessageBubble";
import type { ChatMessage } from "@/lib/store/daemonStore";
import type { MessagePart } from "@/lib/api/types";

const NOW = "2026-05-04T12:30:00.000Z";

function mkMsg(over: Partial<ChatMessage> & { id: string }): ChatMessage {
  const { id, ...rest } = over;
  return {
    id,
    sessionId: "s-1",
    role: "assistant",
    parts: [{ kind: "text" as const, text: "hello" }],
    streaming: false,
    createdAt: NOW,
    updatedAt: NOW,
    ...rest,
  } as ChatMessage;
}

function ToolStub({
  part,
}: {
  part: Extract<MessagePart, { kind: "tool" }>;
}): JSX.Element {
  return (
    <pre className="bg-muted/60 rounded p-2 text-xs overflow-x-auto">
      ▶ {part.toolName}
      {"  "}({part.state})
      {"\n"}
      input: {JSON.stringify(part.input)}
      {part.output ? `\noutput: ${JSON.stringify(part.output)}` : ""}
    </pre>
  );
}

function ReasoningStub({
  part,
}: {
  part: Extract<MessagePart, { kind: "reasoning" }>;
}): JSX.Element {
  return (
    <blockquote className="text-xs italic text-muted-foreground border-l-2 pl-2">
      {part.text}
    </blockquote>
  );
}

function PlanStub({
  part,
}: {
  part: Extract<MessagePart, { kind: "plan" }>;
}): JSX.Element {
  return (
    <ul className="text-sm list-disc pl-4 space-y-1">
      {part.items.map((it) => (
        <li key={it.id} data-status={it.status}>
          {it.text} <span className="text-xs text-muted-foreground">[{it.status}]</span>
        </li>
      ))}
    </ul>
  );
}

const meta: Meta<typeof MessageBubble> = {
  title: "chat/MessageBubble",
  component: MessageBubble,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof MessageBubble>;

const renderProps = {
  renderTool: (p: Extract<MessagePart, { kind: "tool" }>) => <ToolStub part={p} />,
  renderReasoning: (p: Extract<MessagePart, { kind: "reasoning" }>) => (
    <ReasoningStub part={p} />
  ),
  renderPlan: (p: Extract<MessagePart, { kind: "plan" }>) => <PlanStub part={p} />,
};

export const UserText: Story = {
  render: () => (
    <div className="w-[640px]">
      <MessageBubble
        message={mkMsg({
          id: "m1",
          role: "user",
          parts: [{ kind: "text", text: "How does the lanes WAL replay work?" }],
        })}
        streaming={false}
        {...renderProps}
      />
    </div>
  ),
};

export const AssistantWithCost: Story = {
  render: () => (
    <div className="w-[640px]">
      <MessageBubble
        message={mkMsg({
          id: "m2",
          role: "assistant",
          costUsd: 0.0123,
          parts: [
            {
              kind: "text",
              text:
                "It replays from `Last-Event-ID` on reconnect. The journal fsyncs on terminal events so subscribers never see a lost row.",
            },
          ],
        })}
        streaming={false}
        {...renderProps}
      />
    </div>
  ),
};

export const MixedParts: Story = {
  render: () => (
    <div className="w-[640px]">
      <MessageBubble
        message={mkMsg({
          id: "m3",
          role: "assistant",
          parts: [
            { kind: "reasoning", text: "Considering options A vs B…", state: "complete" },
            { kind: "text", text: "Going with **option B** because it composes." },
            {
              kind: "tool",
              toolCallId: "tc-1",
              toolName: "Bash",
              input: { command: "go test ./..." },
              output: "ok",
              state: "output-available",
            },
            {
              kind: "plan",
              items: [
                { id: "1", text: "land item 27", status: "completed" },
                { id: "2", text: "land item 28", status: "in-progress" },
              ],
            },
          ],
        })}
        streaming
        {...renderProps}
      />
    </div>
  ),
};
