// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import { ConnectionLostBanner } from "./ConnectionLostBanner";
import {
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";

const NOOP = { schedule: () => 0, cancel: () => {} };

function mkStore(hardCapped: boolean): DaemonStore {
  const s = createDaemonStore("daemon-storybook", NOOP);
  if (hardCapped) s.getState().setHardCapped(true);
  return s;
}

const meta: Meta<typeof ConnectionLostBanner> = {
  title: "core/ConnectionLostBanner",
  component: ConnectionLostBanner,
  parameters: { layout: "fullscreen" },
};
export default meta;
type Story = StoryObj<typeof ConnectionLostBanner>;

function Demo({ hardCapped }: { hardCapped: boolean }): JSX.Element {
  const [store] = useState(() => mkStore(hardCapped));
  const [reconnects, setReconnects] = useState(0);
  return (
    <div className="w-[860px]">
      <ConnectionLostBanner
        store={store}
        onReconnect={() => {
          setReconnects((n) => n + 1);
          store.getState().setHardCapped(false);
        }}
      />
      <p className="p-3 text-xs text-muted-foreground">
        manual reconnects: {reconnects}
      </p>
    </div>
  );
}

export const HardCapped: Story = {
  render: () => <Demo hardCapped />,
};

export const Healthy: Story = {
  render: () => <Demo hardCapped={false} />,
};
