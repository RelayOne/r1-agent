// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { useState } from "react";
import { SettingsPage } from "./SettingsPage";
import { ThemeProvider } from "@/components/layout/ThemeProvider";
import {
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { Settings } from "@/lib/api/types";

const NOOP = { schedule: () => 0, cancel: () => {} };

const MODELS = [
  { value: "claude-opus-4-7", label: "Opus 4.7" },
  { value: "claude-sonnet-4-6", label: "Sonnet 4.6" },
  { value: "claude-haiku-4-5", label: "Haiku 4.5" },
];

function mkStore(): DaemonStore {
  const s = createDaemonStore("daemon-storybook", NOOP);
  s.getState().hydrateSettings({
    defaultModel: "claude-opus-4-7",
    laneFilters: [],
    theme: "system",
    highContrast: false,
    reducedMotion: false,
    keybindings: {},
  } as Settings);
  return s;
}

const meta: Meta<typeof SettingsPage> = {
  title: "settings/SettingsPage",
  component: SettingsPage,
  parameters: { layout: "fullscreen" },
};
export default meta;
type Story = StoryObj<typeof SettingsPage>;

function Demo(): JSX.Element {
  const [store] = useState(mkStore);
  return (
    <ThemeProvider defaultTheme="light">
      <SettingsPage store={store} availableModels={MODELS} />
    </ThemeProvider>
  );
}

export const Default: Story = {
  render: () => <Demo />,
};
