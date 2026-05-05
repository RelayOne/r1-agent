// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import { GlobalKeybindings } from "./GlobalKeybindings";

const meta: Meta<typeof GlobalKeybindings> = {
  title: "core/GlobalKeybindings",
  component: GlobalKeybindings,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof GlobalKeybindings>;

function Demo(): JSX.Element {
  const [log, setLog] = useState<string[]>([]);
  const push = (s: string): void => setLog((prev) => [...prev.slice(-9), s]);
  return (
    <div className="space-y-2 w-[640px]">
      <p className="text-xs text-muted-foreground">
        Click anywhere outside an input then press a shortcut. The log
        shows the binding that fired.
      </p>
      <GlobalKeybindings
        onSendShortcut={() => push("Send (Mod+Enter)")}
        onInterrupt={() => push("Interrupt (Esc)")}
        onFocusComposer={() => push("Focus composer (/)")}
        onOpenCheatsheet={() => push("Cheat-sheet (?)")}
        onToggleDaemonRail={() => push("Toggle daemon rail (Mod+Shift+S)")}
        onSwitchDaemon={(i) => push(`Switch daemon → index ${i}`)}
      />
      <ol className="text-sm font-mono border border-border rounded p-2 list-none m-0 space-y-1">
        {log.length === 0 ? (
          <li className="text-muted-foreground">no events yet</li>
        ) : (
          log.map((entry, i) => <li key={i}>{entry}</li>)
        )}
      </ol>
    </div>
  );
}

export const Default: Story = {
  render: () => <Demo />,
};
