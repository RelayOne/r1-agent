// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import { NewSessionDialog } from "./NewSessionDialog";
import { Button } from "@/components/ui/button";
import type { CreateSessionRequest } from "@/lib/api/types";

const MODELS = [
  { value: "claude-opus-4-7", label: "Opus 4.7" },
  { value: "claude-sonnet-4-6", label: "Sonnet 4.6" },
  { value: "claude-haiku-4-5", label: "Haiku 4.5" },
];

const PRESETS = [
  { value: "engineer", label: "Engineer (default)" },
  { value: "researcher", label: "Researcher" },
  { value: "reviewer", label: "Reviewer" },
];

function Demo({
  presets,
}: {
  presets?: ReadonlyArray<{ value: string; label: string }>;
}): JSX.Element {
  const [open, setOpen] = useState(true);
  const [created, setCreated] = useState<CreateSessionRequest | null>(null);
  return (
    <div className="space-y-3">
      <Button onClick={() => setOpen(true)}>Open dialog</Button>
      <NewSessionDialog
        open={open}
        onOpenChange={setOpen}
        models={MODELS}
        presets={presets}
        defaultWorkdir="/repo/web"
        onCreate={(p) => {
          setCreated(p);
          return Promise.resolve();
        }}
      />
      {created ? (
        <pre className="text-xs p-2 border border-border rounded bg-muted/40">
          {JSON.stringify(created, null, 2)}
        </pre>
      ) : null}
    </div>
  );
}

const meta: Meta<typeof NewSessionDialog> = {
  title: "session/NewSessionDialog",
  component: NewSessionDialog,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof NewSessionDialog>;

export const Default: Story = {
  render: () => <Demo />,
};

export const WithPresets: Story = {
  render: () => <Demo presets={PRESETS} />,
};
