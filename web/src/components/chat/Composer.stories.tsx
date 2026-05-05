// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import { Composer } from "./Composer";

const meta: Meta<typeof Composer> = {
  title: "chat/Composer",
  component: Composer,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof Composer>;

function Demo({
  streaming,
  characterLimit,
}: {
  streaming?: boolean;
  characterLimit?: number;
}): JSX.Element {
  const [v, setV] = useState("");
  const [last, setLast] = useState<string | null>(null);
  return (
    <div className="w-[640px] space-y-2">
      <Composer
        value={v}
        onChange={setV}
        onSend={(text) => {
          setLast(text);
          setV("");
        }}
        streaming={streaming}
        characterLimit={characterLimit}
      />
      {last ? (
        <pre className="p-2 text-xs border border-border rounded">
          sent → {last}
        </pre>
      ) : null}
    </div>
  );
}

export const Empty: Story = {
  render: () => <Demo />,
};

export const Streaming: Story = {
  render: () => <Demo streaming />,
};

export const WithCharLimit: Story = {
  render: () => <Demo characterLimit={120} />,
};
