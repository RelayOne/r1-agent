// Shared Tailwind preset consumed by both desktop/tailwind.config.js and
// web/tailwind.config.js. Exposes the six lane-status colour tokens
// (D-S1) and a small set of shadcn-friendly defaults so both surfaces
// render lanes identically.
//
// Consumers extend this preset and add surface-specific tokens on top.

/** @type {import('tailwindcss').Config} */
const preset = {
  content: [],
  theme: {
    extend: {
      colors: {
        "lane-pending": "#94a3b8",   // slate-400
        "lane-running": "#3b82f6",   // blue-500
        "lane-blocked": "#f59e0b",   // amber-500
        "lane-done": "#22c55e",      // green-500
        "lane-errored": "#ef4444",   // red-500
        "lane-cancelled": "#6b7280", // gray-500
      },
      fontFamily: {
        mono: [
          "ui-monospace",
          "SFMono-Regular",
          "Menlo",
          "Monaco",
          "Consolas",
          "Liberation Mono",
          "Courier New",
          "monospace",
        ],
      },
      keyframes: {
        "lane-pulse": {
          "0%, 100%": { opacity: "1" },
          "50%": { opacity: "0.4" },
        },
      },
      animation: {
        "lane-pulse": "lane-pulse 1.4s ease-in-out infinite",
      },
    },
  },
  plugins: [],
};

module.exports = preset;
