// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { PlanCard } from "./PlanCard";
import type { MessagePart } from "@/lib/api/types";

type PlanPart = Extract<MessagePart, { kind: "plan" }>;

const ramping: PlanPart = {
  kind: "plan",
  items: [
    { id: "1", text: "Scaffold the web/ workspace", status: "completed" },
    { id: "2", text: "Wire ResilientSocket + AuthClient", status: "completed" },
    { id: "3", text: "Land R1dClient public surface", status: "completed" },
    { id: "4", text: "Build the component tree", status: "in-progress" },
    { id: "5", text: "Author Vitest + Playwright suites", status: "pending" },
    { id: "6", text: "CI gate the npm build", status: "pending" },
  ],
};

const blocked: PlanPart = {
  kind: "plan",
  items: [
    { id: "1", text: "Add tauri-plugin-websocket", status: "completed" },
    { id: "2", text: "Wire macOS notarization", status: "blocked" },
    { id: "3", text: "Author auto-update channel", status: "pending" },
    { id: "4", text: "Old plan: lipo + universal binary", status: "skipped" },
  ],
};

const meta: Meta<typeof PlanCard> = {
  title: "chat/PlanCard",
  component: PlanCard,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof PlanCard>;

function Wrap({ part }: { part: PlanPart }): JSX.Element {
  return (
    <div className="w-[640px]">
      <PlanCard part={part} />
    </div>
  );
}

export const Ramping: Story = {
  render: () => <Wrap part={ramping} />,
};

export const Blocked: Story = {
  render: () => <Wrap part={blocked} />,
};

export const Empty: Story = {
  render: () => <Wrap part={{ kind: "plan", items: [] }} />,
};
