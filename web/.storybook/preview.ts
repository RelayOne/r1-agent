// web/.storybook/preview.ts — Storybook 9 preview config per
// specs/agentic-test-harness.md §7 + §12 item 31.
//
// The preview is intentionally minimal: a11y addon parameters
// (default rules + the "agentic" custom param the lint scanner
// reads) live inside each story file's `parameters` block, not
// here, so per-component contracts stay co-located with the
// component.
import type { Preview } from "@storybook/react";

const preview: Preview = {
  parameters: {
    actions: { argTypesRegex: "^on[A-Z].*" },
    controls: {
      matchers: {
        color: /(background|color)$/i,
        date: /Date$/i,
      },
    },
    a11y: {
      // Default a11y rules — every story can override or extend.
      element: "#storybook-root",
      config: {},
      options: {},
      manual: false,
    },
  },
};

export default preview;
