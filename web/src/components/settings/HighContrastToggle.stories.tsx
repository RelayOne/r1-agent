// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { HighContrastToggle } from "./HighContrastToggle";
import { ThemeProvider } from "@/components/layout/ThemeProvider";

const meta: Meta<typeof HighContrastToggle> = {
  title: "settings/HighContrastToggle",
  component: HighContrastToggle,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof HighContrastToggle>;

export const FromLight: Story = {
  render: () => (
    <ThemeProvider defaultTheme="light">
      <HighContrastToggle />
    </ThemeProvider>
  ),
};

export const FromDark: Story = {
  render: () => (
    <ThemeProvider defaultTheme="dark">
      <HighContrastToggle />
    </ThemeProvider>
  ),
};

export const StartingHc: Story = {
  render: () => (
    <ThemeProvider defaultTheme="hc">
      <HighContrastToggle />
    </ThemeProvider>
  ),
};
