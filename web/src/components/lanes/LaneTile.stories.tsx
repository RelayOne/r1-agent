// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useEffect, useState } from "react";
import { LaneTile } from "./LaneTile";
import {
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { LaneSnapshot } from "@/lib/api/types";

const NOOP = { schedule: () => 0, cancel: () => {} };
const NOW = "2026-05-04T12:30:00.000Z";

function mkLane(over: Partial<LaneSnapshot> & { id: string }): LaneSnapshot {
  const { id, ...rest } = over;
  return {
    id,
    sessionId: "s-1",
    label: `lane ${id}`,
    state: "running",
    createdAt: NOW,
    updatedAt: NOW,
    progress: null,
    lastRender: "",
    lastSeq: 0,
    ...rest,
  } as LaneSnapshot;
}

function mkStore(lastRender: string): DaemonStore {
  const s = createDaemonStore("daemon-storybook", NOOP);
  s.getState().hydrateLanes("s-1", [
    mkLane({ id: "l1", label: "research", state: "running", lastRender }),
  ]);
  return s;
}

const meta: Meta<typeof LaneTile> = {
  title: "lanes/LaneTile",
  component: LaneTile,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof LaneTile>;

const STATIC_RENDER = [
  "▶ Read internal/cortex/lane.go",
  "    1: package cortex",
  "    2:",
  "    3: type Lane struct {",
  "    4:   id     string",
  "    5:   state  LaneState",
  "    6: }",
  "✓ done in 18 ms",
].join("\n");

function StreamingDemo(): JSX.Element {
  const [store] = useState(() => mkStore("starting…"));
  useEffect(() => {
    let i = 0;
    const id = window.setInterval(() => {
      i += 1;
      const next = `${STATIC_RENDER}\n— frame ${i} —`;
      store.setState((prev) => ({
        ...prev,
        lanes: {
          ...prev.lanes,
          byKey: {
            ...prev.lanes.byKey,
            "s-1:l1": { ...prev.lanes.byKey["s-1:l1"], lastRender: next },
          },
        },
      }));
      if (i > 10) window.clearInterval(id);
    }, 600);
    return () => window.clearInterval(id);
  }, [store]);
  return (
    <div className="w-[420px] h-[280px] border border-border rounded-md overflow-hidden">
      <LaneTile
        store={store}
        sessionId="s-1"
        laneId="l1"
        onUnpin={() => {}}
        onKill={() => {}}
        onFocus={() => {}}
      />
    </div>
  );
}

export const Static: Story = {
  render: () => {
    const [store] = useState(() => mkStore(STATIC_RENDER));
    return (
      <div className="w-[420px] h-[280px] border border-border rounded-md overflow-hidden">
        <LaneTile
          store={store}
          sessionId="s-1"
          laneId="l1"
          onUnpin={() => {}}
          onKill={() => {}}
          onFocus={() => {}}
        />
      </div>
    );
  },
};

export const Streaming: Story = {
  render: () => <StreamingDemo />,
};
