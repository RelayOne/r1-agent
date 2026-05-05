// web/.storybook/main.ts — Storybook 9 configuration scaffold per
// specs/agentic-test-harness.md §7 + §12 item 31.
//
// STATUS: scaffold-only — no story files exist yet because spec 6
// (web-chat-ui) has not merged into this checkpoint. The CI lint
// at §8 expects every web/src/components/*.tsx to ship with a
// matching *.stories.tsx; that wiring lands when spec 6 brings
// the component sources here.
//
// This file lists the directories Storybook will eventually walk
// once the component sources land. Adding stories under those
// paths is sufficient — no further config edits required.
import type { StorybookConfig } from "@storybook/react-vite";

const config: StorybookConfig = {
  stories: [
    "../src/components/**/*.stories.@(ts|tsx)",
    "../src/screens/**/*.stories.@(ts|tsx)",
  ],
  addons: [
    "@storybook/addon-a11y",
    "@storybook/addon-essentials",
    "@storybook/addon-interactions",
  ],
  framework: {
    name: "@storybook/react-vite",
    options: {},
  },
  docs: {
    autodocs: "tag",
  },
};

export default config;
