// SPDX-License-Identifier: MIT
import type { Meta, StoryObj } from "@/test/storybook-types";
import { ThemeProvider, useTheme } from "./ThemeProvider";

function Demo(): JSX.Element {
  const { theme, resolvedDark, resolvedHighContrast, setTheme } = useTheme();
  return (
    <div className="p-4 space-y-2 bg-background text-foreground border rounded-md">
      <p data-testid="theme-state">
        theme=<b>{theme}</b> dark=<b>{String(resolvedDark)}</b> hc=
        <b>{String(resolvedHighContrast)}</b>
      </p>
      <div className="flex gap-2 flex-wrap">
        {(["light", "dark", "hc", "system"] as const).map((mode) => (
          <button
            key={mode}
            onClick={() => setTheme(mode)}
            data-testid={`theme-set-${mode}`}
            aria-label={`Set theme to ${mode}`}
            className="px-2 py-1 border rounded-md"
          >
            {mode}
          </button>
        ))}
      </div>
    </div>
  );
}

const meta: Meta<typeof ThemeProvider> = {
  title: "layout/ThemeProvider",
  component: ThemeProvider,
  parameters: { layout: "centered" },
};
export default meta;
type Story = StoryObj<typeof ThemeProvider>;

export const Default: Story = {
  render: () => (
    <ThemeProvider>
      <Demo />
    </ThemeProvider>
  ),
};

export const StartDark: Story = {
  render: () => (
    <ThemeProvider defaultTheme="dark">
      <Demo />
    </ThemeProvider>
  ),
};

export const StartHighContrast: Story = {
  render: () => (
    <ThemeProvider defaultTheme="hc">
      <Demo />
    </ThemeProvider>
  ),
};
