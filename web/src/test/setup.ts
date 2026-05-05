// SPDX-License-Identifier: MIT
// Vitest setup file. Wires @testing-library/jest-dom matchers and
// MSW (mock service worker) for HTTP interception during unit tests.
//
// Real MSW handlers will land in later items (component tests + WS
// fixtures). This file establishes the lifecycle hooks.
import "@testing-library/jest-dom/vitest";
import { afterAll, afterEach, beforeAll } from "vitest";
import { setupServer } from "msw/node";

// Empty handler list now; component tests register handlers ad-hoc
// via server.use(...) per spec §Test Plan.
export const server = setupServer();

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());
