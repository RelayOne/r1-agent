// SPDX-License-Identifier: MIT
// Global vitest setup for R1 Desktop panel tests.
import { afterEach } from "vitest";

// React 19's concurrent scheduler defers render work through setImmediate
// chains. A test that mounts a React root (session-view's lane rail, the
// discovery wizard) and returns before that work drains lets it fire after
// vitest tears down the jsdom window, crashing the run with
// "ReferenceError: window is not defined" — vitest 4 fails the suite on
// unhandled errors even when every test passes. Drain the macrotask queue
// while the window still exists so pending React work lands harmlessly.
afterEach(async () => {
  for (let i = 0; i < 10; i++) {
    await new Promise<void>((resolve) => {
      setImmediate(resolve);
    });
  }
});
