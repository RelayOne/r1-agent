// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import { StopButton } from "./StopButton";

const meta: Meta<typeof StopButton> = {
  title: "chat/StopButton",
  component: StopButton,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof StopButton>;

function Demo({ keepPartial }: { keepPartial?: boolean }): JSX.Element {
  const [streaming, setStreaming] = useState(true);
  const [last, setLast] = useState<boolean | null>(null);
  return (
    <div className="space-y-2">
      <div className="flex gap-2 items-center">
        <button
          className="px-2 py-1 border rounded text-sm"
          onClick={() => setStreaming((s) => !s)}
        >
          toggle streaming ({streaming ? "on" : "off"})
        </button>
        <StopButton
          streaming={streaming}
          dropPartial={!keepPartial}
          onInterrupt={(drop) => {
            setLast(drop);
            setStreaming(false);
          }}
        />
      </div>
      {last !== null ? (
        <p className="text-xs text-muted-foreground">
          last interrupt sent dropPartial={String(last)}
        </p>
      ) : null}
    </div>
  );
}

export const StreamingDropPartial: Story = {
  render: () => <Demo />,
};

export const StreamingKeepPartial: Story = {
  render: () => <Demo keepPartial />,
};
