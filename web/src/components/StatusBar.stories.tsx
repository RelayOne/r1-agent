// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import { StatusBar } from "./StatusBar";
import {
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { LaneSnapshot, SessionMetadata } from "@/lib/api/types";

const NOOP = { schedule: () => 0, cancel: () => {} };
const NOW = "2026-05-04T12:30:00.000Z";

function mkLane(id: string, state: LaneSnapshot["state"]): LaneSnapshot {
  return {
    id,
    sessionId: "s-1",
    label: id,
    state,
    createdAt: NOW,
    updatedAt: NOW,
    progress: null,
    lastRender: null,
    lastSeq: 0,
  };
}

function mkSession(id: string, costUsd: number): SessionMetadata {
  return {
    id,
    title: id,
    workdir: "/repo",
    model: "claude",
    status: "idle",
    createdAt: NOW,
    updatedAt: NOW,
    lastActivityAt: null,
    costUsd,
    laneCount: 0,
    systemPromptPreset: null,
  };
}

function mkStore(): DaemonStore {
  const s = createDaemonStore("daemon-storybook", NOOP);
  s.getState().setConnectionState("open");
  s.getState().hydrateSessions([mkSession("s-1", 0.0123)]);
  s.getState().hydrateLanes("s-1", [
    mkLane("l1", "running"),
    mkLane("l2", "waiting-tool"),
    mkLane("l3", "completed"),
    mkLane("l4", "failed"),
  ]);
  return s;
}

const meta: Meta<typeof StatusBar> = {
  title: "core/StatusBar",
  component: StatusBar,
  parameters: { layout: "fullscreen" },
};
export default meta;
type Story = StoryObj<typeof StatusBar>;

function Demo({ latencyMs }: { latencyMs?: number | null }): JSX.Element {
  const [store] = useState(mkStore);
  return (
    <div className="w-[960px] border border-border rounded-md overflow-hidden">
      <StatusBar store={store} sessionId="s-1" latencyMs={latencyMs} />
    </div>
  );
}

export const Connected: Story = {
  render: () => <Demo latencyMs={42} />,
};

export const Reconnecting: Story = {
  render: () => {
    const [store] = useState(() => {
      const s = mkStore();
      s.getState().setConnectionState("reconnecting");
      return s;
    });
    return (
      <div className="w-[960px] border border-border rounded-md overflow-hidden">
        <StatusBar store={store} sessionId="s-1" latencyMs={null} />
      </div>
    );
  },
};

export const Disconnected: Story = {
  render: () => {
    const [store] = useState(() => {
      const s = mkStore();
      s.getState().setConnectionState("closed");
      return s;
    });
    return (
      <div className="w-[960px] border border-border rounded-md overflow-hidden">
        <StatusBar store={store} sessionId="s-1" latencyMs={null} />
      </div>
    );
  },
};
