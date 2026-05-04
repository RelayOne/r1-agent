// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { ReasoningCard } from "./ReasoningCard";
import type { MessagePart } from "@/lib/api/types";

type ReasoningPart = Extract<MessagePart, { kind: "reasoning" }>;

const streaming: ReasoningPart = {
  kind: "reasoning",
  text:
    "Weighing the tradeoffs between virtualizing the message log with @tanstack/react-virtual versus a simpler ScrollArea + memoized rows. Virtualization wins on long sessions; complexity is acceptable.",
  state: "streaming",
};

const complete: ReasoningPart = {
  kind: "reasoning",
  text:
    "Decided to ship the virtualized log with a sticky-bottom anchor. The user can scroll up to break the anchor; new rows append without snapping the viewport.",
  state: "complete",
};

const meta: Meta<typeof ReasoningCard> = {
  title: "chat/ReasoningCard",
  component: ReasoningCard,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof ReasoningCard>;

function Wrap({ part }: { part: ReasoningPart }): JSX.Element {
  return (
    <div className="w-[640px]">
      <ReasoningCard part={part} />
    </div>
  );
}

export const Streaming: Story = {
  render: () => <Wrap part={streaming} />,
};

export const Complete: Story = {
  render: () => <Wrap part={complete} />,
};

export const StreamingReducedMotion: Story = {
  render: () => (
    <div className="w-[640px]">
      <ReasoningCard part={streaming} reducedMotionOverride />
    </div>
  ),
};
