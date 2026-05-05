// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import { TileGrid } from "./TileGrid";
import {
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { LaneSnapshot } from "@/lib/api/types";

const NOOP = { schedule: () => 0, cancel: () => {} };
const NOW = "2026-05-04T12:30:00.000Z";

function mkLane(id: string, label: string): LaneSnapshot {
  return {
    id,
    sessionId: "s-1",
    label,
    state: "running",
    createdAt: NOW,
    updatedAt: NOW,
    progress: null,
    lastRender: `▶ live output for ${label}\nstep 1 done\nstep 2 in progress…`,
    lastSeq: 0,
  };
}

function mkStore(n: number): DaemonStore {
  const labels = ["main", "research", "scratch", "archive"];
  const ids = Array.from({ length: n }, (_, i) => `lane-${i + 1}`);
  const s = createDaemonStore("daemon-storybook", NOOP);
  s.getState().hydrateLanes(
    "s-1",
    ids.map((id, i) => mkLane(id, labels[i] ?? id)),
  );
  for (const id of ids) s.getState().pinLane("s-1", id);
  return s;
}

const meta: Meta<typeof TileGrid> = {
  title: "lanes/TileGrid",
  component: TileGrid,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof TileGrid>;

function Demo({ count }: { count: number }): JSX.Element {
  const [store] = useState(() => mkStore(count));
  return (
    <div className="w-[860px] h-[520px] border border-border rounded-md overflow-hidden bg-background">
      <TileGrid
        store={store}
        sessionId="s-1"
        onKill={() => {}}
        onFocusLane={() => {}}
      />
    </div>
  );
}

export const Single: Story = { render: () => <Demo count={1} /> };
export const Two: Story = { render: () => <Demo count={2} /> };
export const Three: Story = { render: () => <Demo count={3} /> };
export const Four: Story = { render: () => <Demo count={4} /> };
export const Empty: Story = {
  render: () => {
    const [store] = useState(() => createDaemonStore("daemon-empty", NOOP));
    return (
      <div className="w-[420px] h-[200px] border border-border rounded-md overflow-hidden bg-background">
        <TileGrid store={store} sessionId="s-1" onKill={() => {}} />
      </div>
    );
  },
};
