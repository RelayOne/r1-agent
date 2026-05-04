// SPDX-License-Identifier: MIT
//
// Thin shim that hands a Playwright Page-like surface to the desktop
// e2e specs. CI wires this to `tauri-driver` (a WebDriver-compatible
// daemon Tauri ships for end-to-end testing); local dev runs the
// same surface against a Playwright-managed Electron-style binary.
//
// The shim's job is purely lifecycle — start a driver session,
// expose `click` / `testState` / `close`, and tear down deterministic
// processes on the way out.

import { spawn, type ChildProcessByStdio } from "node:child_process";
import * as readline from "node:readline";

import type {
  DesktopApp,
  LaunchOptions,
} from "./desktop-fixtures";

const DRIVER_PORT_ENV = "R1_TAURI_DRIVER_PORT";

/** Launch the desktop app under tauri-driver, returning a
 *  Page-like handle. Cleaning up the returned handle (`close`)
 *  terminates the driver process. */
export async function launch(opts: LaunchOptions): Promise<DesktopApp> {
  const driverPort = process.env[DRIVER_PORT_ENV];
  if (!driverPort) {
    throw new Error(
      `${DRIVER_PORT_ENV} not set — start tauri-driver first or run via the e2e CI job`,
    );
  }

  // The CI job sets R1_DESKTOP_BINARY to the freshly built bundle;
  // local dev defaults to the cargo target/release path.
  const binaryPath =
    opts.binaryPath ?? process.env.R1_DESKTOP_BINARY ?? "../../target/release/r1-desktop";

  const driver = spawn(binaryPath, [], {
    env: {
      ...process.env,
      R1_E2E: "1",
      R1_FAKE_EXTERNAL_DAEMON: opts.withFakeExternalDaemon ? "1" : "",
    },
    stdio: ["ignore", "pipe", "pipe"],
  });

  // Pipe driver stdout through readline so the test runner can
  // surface log output deterministically.
  const lines = readline.createInterface({ input: driver.stdout });
  lines.on("line", (line) => process.stdout.write(`[driver] ${line}\n`));

  // Build the Page-like handle. The methods route through
  // tauri-driver's WebDriver session bound on $DRIVER_PORT_ENV;
  // each issues a single HTTP request.
  return makeHandle(driver);
}

function makeHandle(driver: ChildProcessByStdio<null, NodeJS.ReadableStream, NodeJS.ReadableStream>): DesktopApp {
  let closed = false;

  return {
    async click(selector) {
      assertNotClosed(closed);
      await driverRequest("click", { selector });
    },

    async waitForEvent<T = unknown>(name: string, timeoutMs: number): Promise<T> {
      assertNotClosed(closed);
      const payload = (await driverRequest("waitForEvent", {
        name,
        timeoutMs,
      })) as T;
      return payload;
    },

    async testState<T>(): Promise<T> {
      assertNotClosed(closed);
      return (await driverRequest("testState", {})) as T;
    },

    async close() {
      if (closed) return;
      closed = true;
      driver.kill("SIGTERM");
      await new Promise<void>((resolve) => {
        const timer = setTimeout(() => {
          driver.kill("SIGKILL");
          resolve();
        }, 2000);
        driver.once("exit", () => {
          clearTimeout(timer);
          resolve();
        });
      });
    },
  };
}

function assertNotClosed(closed: boolean): void {
  if (closed) {
    throw new Error("DesktopApp handle used after close()");
  }
}

/** One-shot HTTP request to the tauri-driver session. */
async function driverRequest(
  command: string,
  body: Record<string, unknown>,
): Promise<unknown> {
  const port = process.env[DRIVER_PORT_ENV];
  const url = `http://127.0.0.1:${port}/${command}`;
  const response = await fetch(url, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw new Error(`tauri-driver ${command} failed: ${response.status}`);
  }
  return response.json();
}
