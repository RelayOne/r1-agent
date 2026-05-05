// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import { ChatPane } from "./ChatPane";
import { Button } from "@/components/ui/button";
import {
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";

const NOOP = { schedule: () => 0, cancel: () => {} };

function mkStore(): DaemonStore {
  return createDaemonStore("daemon-storybook", NOOP);
}

function MessageColumn(): JSX.Element {
  return (
    <div className="h-full flex flex-col p-4 gap-3 overflow-y-auto">
      <article className="rounded-md border border-border p-3">
        <header className="text-xs text-muted-foreground mb-1">user · 12:01</header>
        <p>Spin up a session and start working on item 25.</p>
      </article>
      <article className="rounded-md border border-border p-3 bg-muted/40">
        <header className="text-xs text-muted-foreground mb-1">assistant · 12:01</header>
        <p>
          ChatPane now switches into a tile grid the moment you pin a lane.
          Until then it shows the conversation.
        </p>
      </article>
      <div className="border-t border-border pt-2 mt-auto text-xs text-muted-foreground">
        composer slot · Cmd+Enter to send
      </div>
    </div>
  );
}

function TileGrid({
  laneIds,
}: {
  laneIds: ReadonlyArray<string>;
}): JSX.Element {
  return (
    <div className="grid grid-cols-2 gap-2 p-2 h-full">
      {laneIds.map((id) => (
        <div
          key={id}
          className="rounded-md border border-border bg-muted/20 flex items-center justify-center text-sm"
        >
          {id}
        </div>
      ))}
    </div>
  );
}

function Demo({ initiallyPinned }: { initiallyPinned: boolean }): JSX.Element {
  const [store] = useState(() => {
    const s = mkStore();
    if (initiallyPinned) {
      s.getState().pinLane("s-1", "lane-a");
      s.getState().pinLane("s-1", "lane-b");
    }
    return s;
  });
  return (
    <div className="space-y-2">
      <div className="flex gap-2">
        <Button
          size="sm"
          onClick={() => store.getState().pinLane("s-1", `lane-${Math.floor(Math.random() * 9999)}`)}
        >
          Pin a lane
        </Button>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => {
            const ids = store.getState().ui.tilePinnedBySession["s-1"] ?? [];
            ids.forEach((id) => store.getState().unpinLane("s-1", id));
          }}
        >
          Clear pins
        </Button>
      </div>
      <div className="h-[420px] w-[720px] border border-border rounded-md overflow-hidden">
        <ChatPane
          store={store}
          sessionId="s-1"
          renderMessageColumn={() => <MessageColumn />}
          renderTileGrid={(_sid, ids) => <TileGrid laneIds={ids} />}
        />
      </div>
    </div>
  );
}

const meta: Meta<typeof ChatPane> = {
  title: "chat/ChatPane",
  component: ChatPane,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof ChatPane>;

export const Conversation: Story = {
  render: () => <Demo initiallyPinned={false} />,
};

export const TileMode: Story = {
  render: () => <Demo initiallyPinned />,
};
