// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import { LanesSidebar } from "./LanesSidebar";
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
    lastRender: null,
    lastSeq: 0,
    ...rest,
  } as LaneSnapshot;
}

function mkStore(): DaemonStore {
  const s = createDaemonStore("daemon-storybook", NOOP);
  s.getState().hydrateLanes("s-1", [
    mkLane({ id: "l1", label: "main", state: "running", progress: 0.7 }),
    mkLane({ id: "l2", label: "research", state: "waiting-tool", progress: 0.3 }),
    mkLane({ id: "l3", label: "scratch", state: "completed", progress: 1 }),
    mkLane({ id: "l4", label: "broken", state: "failed", progress: null }),
    mkLane({ id: "l5", label: "queued one", state: "queued", progress: null }),
    mkLane({ id: "l6", label: "killed one", state: "killed", progress: null }),
  ]);
  // Pre-pin the second lane so the story shows the pinned state.
  s.getState().pinLane("s-1", "l2");
  return s;
}

const meta: Meta<typeof LanesSidebar> = {
  title: "lanes/LanesSidebar",
  component: LanesSidebar,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof LanesSidebar>;

function Demo(): JSX.Element {
  const [store] = useState(mkStore);
  const [killed, setKilled] = useState<string | null>(null);
  return (
    <div className="space-y-2">
      <div className="w-72 h-[420px] border border-border rounded-md overflow-hidden bg-background">
        <LanesSidebar
          store={store}
          sessionId="s-1"
          onKill={(id) => setKilled(id)}
        />
      </div>
      {killed ? (
        <p className="text-xs text-muted-foreground">last kill → {killed}</p>
      ) : null}
    </div>
  );
}

export const Populated: Story = {
  render: () => <Demo />,
};

export const Empty: Story = {
  render: () => {
    const [store] = useState(() => createDaemonStore("daemon-empty", NOOP));
    return (
      <div className="w-72 h-[200px] border border-border rounded-md overflow-hidden bg-background">
        <LanesSidebar store={store} sessionId="s-1" onKill={() => {}} />
      </div>
    );
  },
};
