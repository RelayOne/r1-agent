// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import { ThreeColumnShell } from "./ThreeColumnShell";
import {
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";

const NOOP = { schedule: () => 0, cancel: () => {} };

function mkStore(): DaemonStore {
  return createDaemonStore("daemon-storybook", NOOP);
}

function SessionsList(): JSX.Element {
  return (
    <nav className="p-3 text-sm space-y-1">
      <p className="font-semibold mb-2 text-xs uppercase tracking-wide text-muted-foreground">
        Sessions
      </p>
      <ul className="space-y-1" aria-label="Session list">
        <li>
          <a className="block px-2 py-1 rounded hover:bg-muted" href="#">
            spec/web-chat-ui · 4 min ago
          </a>
        </li>
        <li>
          <a className="block px-2 py-1 rounded hover:bg-muted" href="#">
            r1d-server cleanup · 2 hr ago
          </a>
        </li>
        <li>
          <a className="block px-2 py-1 rounded hover:bg-muted" href="#">
            cortex-core review · yesterday
          </a>
        </li>
      </ul>
    </nav>
  );
}

function ChatPaneSample(): JSX.Element {
  return (
    <div className="h-full flex flex-col p-4 gap-3 overflow-y-auto">
      <article className="rounded-md border border-border p-3">
        <header className="text-xs text-muted-foreground mb-1">user · 12:01</header>
        <p>Add the ThreeColumnShell layout.</p>
      </article>
      <article className="rounded-md border border-border p-3 bg-muted/40">
        <header className="text-xs text-muted-foreground mb-1">assistant · 12:01</header>
        <p>
          Done — collapsible left and right rails, persisted via the
          per-daemon zustand store.
        </p>
      </article>
    </div>
  );
}

function LanesList(): JSX.Element {
  return (
    <aside className="p-3 text-sm space-y-2">
      <p className="font-semibold mb-2 text-xs uppercase tracking-wide text-muted-foreground">
        Lanes
      </p>
      <ul className="space-y-1" aria-label="Lane list">
        <li className="flex items-center gap-2">
          <span className="inline-block w-2 h-2 rounded-full bg-emerald-500" />
          <span>main</span>
        </li>
        <li className="flex items-center gap-2">
          <span className="inline-block w-2 h-2 rounded-full bg-amber-500" />
          <span>research</span>
        </li>
        <li className="flex items-center gap-2">
          <span className="inline-block w-2 h-2 rounded-full bg-slate-400" />
          <span>scratch</span>
        </li>
      </ul>
    </aside>
  );
}

function Demo({ store }: { store: DaemonStore }): JSX.Element {
  return (
    <div className="h-[480px] w-[960px] border border-border rounded-md overflow-hidden">
      <ThreeColumnShell
        store={store}
        left={<SessionsList />}
        center={<ChatPaneSample />}
        right={<LanesList />}
      />
    </div>
  );
}

const meta: Meta<typeof ThreeColumnShell> = {
  title: "layout/ThreeColumnShell",
  component: ThreeColumnShell,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof ThreeColumnShell>;

export const Default: Story = {
  render: () => {
    const [store] = useState(mkStore);
    return <Demo store={store} />;
  },
};

export const LeftCollapsed: Story = {
  render: () => {
    const [store] = useState(() => {
      const s = mkStore();
      s.getState().setLeftRailCollapsed(true);
      return s;
    });
    return <Demo store={store} />;
  },
};

export const BothCollapsed: Story = {
  render: () => {
    const [store] = useState(() => {
      const s = mkStore();
      s.getState().setLeftRailCollapsed(true);
      s.getState().setRightRailCollapsed(true);
      return s;
    });
    return <Demo store={store} />;
  },
};
