// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import { SessionList } from "./SessionList";
import {
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { SessionMetadata } from "@/lib/api/types";

const NOOP = { schedule: () => 0, cancel: () => {} };
const NOW = new Date("2026-05-04T12:30:00.000Z");

function seed(): SessionMetadata[] {
  return [
    {
      id: "s-1",
      title: "scaffold web ui",
      workdir: "/repo/web",
      model: "claude-opus-4-7",
      status: "running",
      createdAt: "2026-05-04T11:50:00.000Z",
      updatedAt: "2026-05-04T12:29:55.000Z",
      lastActivityAt: "2026-05-04T12:29:55.000Z",
      costUsd: 0.42,
      laneCount: 3,
      systemPromptPreset: null,
    },
    {
      id: "s-2",
      title: "review lanes-protocol diff",
      workdir: "/repo/internal/cortex",
      model: "claude-opus-4-7",
      status: "thinking",
      createdAt: "2026-05-04T11:40:00.000Z",
      updatedAt: "2026-05-04T12:24:00.000Z",
      lastActivityAt: "2026-05-04T12:24:00.000Z",
      costUsd: 0.18,
      laneCount: 1,
      systemPromptPreset: null,
    },
    {
      id: "s-3",
      title: null,
      workdir: "/repo/cmd/r1-bench",
      model: "claude-haiku-4-5",
      status: "waiting",
      createdAt: "2026-05-04T08:10:00.000Z",
      updatedAt: "2026-05-04T11:10:00.000Z",
      lastActivityAt: "2026-05-04T11:10:00.000Z",
      costUsd: 0.04,
      laneCount: 0,
      systemPromptPreset: null,
    },
    {
      id: "s-4",
      title: "yesterday's repro",
      workdir: "/repo",
      model: "claude-opus-4-7",
      status: "error",
      createdAt: "2026-05-03T14:00:00.000Z",
      updatedAt: "2026-05-03T16:00:00.000Z",
      lastActivityAt: "2026-05-03T16:00:00.000Z",
      costUsd: 1.2,
      laneCount: 0,
      systemPromptPreset: null,
    },
    {
      id: "s-5",
      title: "ledger compaction PR",
      workdir: "/repo/internal/ledger",
      model: "claude-opus-4-7",
      status: "completed",
      createdAt: "2026-05-04T09:00:00.000Z",
      updatedAt: "2026-05-04T10:30:00.000Z",
      lastActivityAt: "2026-05-04T10:30:00.000Z",
      costUsd: 0.66,
      laneCount: 0,
      systemPromptPreset: null,
    },
  ];
}

function mkStore(): DaemonStore {
  const s = createDaemonStore("daemon-storybook", NOOP);
  s.getState().hydrateSessions(seed());
  return s;
}

function Demo({ active }: { active: string | null }): JSX.Element {
  const [store] = useState(mkStore);
  const [selected, setSelected] = useState<string | null>(active);
  return (
    <div className="w-72 h-[480px] border border-border rounded-md overflow-hidden bg-background">
      <SessionList
        store={store}
        activeSessionId={selected}
        onSelect={setSelected}
        now={NOW}
      />
    </div>
  );
}

const meta: Meta<typeof SessionList> = {
  title: "session/SessionList",
  component: SessionList,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof SessionList>;

export const Populated: Story = {
  render: () => <Demo active="s-1" />,
};

export const NoActiveSelection: Story = {
  render: () => <Demo active={null} />,
};

export const Empty: Story = {
  render: () => {
    const [store] = useState(() => createDaemonStore("daemon-empty", NOOP));
    return (
      <div className="w-72 h-[200px] border border-border rounded-md overflow-hidden bg-background">
        <SessionList
          store={store}
          activeSessionId={null}
          onSelect={() => {}}
          now={NOW}
        />
      </div>
    );
  },
};
