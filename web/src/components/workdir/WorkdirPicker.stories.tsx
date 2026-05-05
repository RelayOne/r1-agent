// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import {
  WorkdirBadge,
  WorkdirPickerDialog,
} from "./WorkdirPicker";

const meta: Meta<typeof WorkdirBadge> = {
  title: "workdir/WorkdirPicker",
  component: WorkdirBadge,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof WorkdirBadge>;

function Demo({
  fsaAvailable,
}: {
  fsaAvailable: boolean;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  const [workdir, setWorkdir] = useState<string | null>("/repo/web");
  return (
    <div className="space-y-2">
      <WorkdirBadge workdir={workdir} onOpenPicker={() => setOpen(true)} />
      <WorkdirPickerDialog
        open={open}
        onOpenChange={setOpen}
        defaultPath={workdir ?? ""}
        onSelect={(p) => {
          setWorkdir(p);
        }}
        listAllowedRoots={() =>
          Promise.resolve([
            "/repo/web",
            "/repo/internal",
            "/repo/desktop",
            "/repo/cmd/r1",
          ])
        }
        showDirectoryPicker={
          fsaAvailable
            ? () => Promise.resolve({ name: "scratch" } as FileSystemDirectoryHandle)
            : undefined
        }
      />
    </div>
  );
}

export const WithFsa: Story = {
  render: () => <Demo fsaAvailable />,
};

export const FallbackOnly: Story = {
  render: () => <Demo fsaAvailable={false} />,
};
