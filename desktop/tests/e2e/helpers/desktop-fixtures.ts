// SPDX-License-Identifier: MIT
//
// Playwright + tauri-driver test fixtures for the desktop e2e suite.
//
// Spec desktop-cortex-augmentation §11.3 + items 34..37. The
// fixtures wrap the boilerplate of:
//
//   * Launching a Tauri-driver-controlled R1 Desktop instance with
//     environment overrides (HOME pointing at a fresh temp dir,
//     R1_BINARY_PATH pointing at a test sidecar binary, etc.).
//   * Polling for the DaemonStatus pill to reach a target state.
//   * Reading + writing ~/.r1/daemon.json so individual tests can
//     pre-stage the discovery surface.
//
// CI binds these to the real `tauri-driver` server; local dev runs
// them against the same surface so the same spec passes in both
// environments. tauri-driver and @playwright/test are devDeps the
// desktop/ package will pin once the e2e job is enabled (the
// "desktop-augmentation.yml" workflow added in item 38 turns it
// on).

import { promises as fs } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

/** A handle returned by launchDesktopApp; mirrors a subset of the
 *  Playwright Page interface so individual specs feel uniform.
 */
export interface DesktopApp {
  /** Click an element identified by a CSS selector. */
  click(selector: string): Promise<void>;
  /** Wait for a custom event payload from the WebView. */
  waitForEvent<T = unknown>(name: string, timeoutMs: number): Promise<T>;
  /** Read a value the host emits via `app.emit("test://state", value)`. */
  testState<T>(): Promise<T>;
  /** Tear down the app + driver session. */
  close(): Promise<void>;
}

export interface LaunchOptions {
  /** Pre-populate ~/.r1/daemon.json + bind a fake loopback listener. */
  withFakeExternalDaemon?: boolean;
  /** Override the test binary path (defaults to tauri-driver discovery). */
  binaryPath?: string;
}

export interface BannerState {
  kind: "external" | "sidecar" | "reconnecting" | "offline";
  url?: string;
  reason?: string;
}

export interface DaemonJsonInfo {
  url: string;
  token: string;
  pid?: number;
}

// -------------------------------------------------------------------
// Daemon-json fixtures
// -------------------------------------------------------------------

/** Path to the test-mode daemon.json. Honours `R1_TEST_HOME` when
 *  set so multiple tests can run in parallel without sharing state.
 */
function daemonJsonPath(): string {
  const home = process.env.R1_TEST_HOME ?? os.homedir();
  return path.join(home, ".r1", "daemon.json");
}

/** Remove ~/.r1/daemon.json if present. Safe to call repeatedly. */
export async function ensureNoDaemonJson(): Promise<void> {
  const p = daemonJsonPath();
  try {
    await fs.unlink(p);
  } catch (err) {
    // ENOENT is fine — that's the desired post-condition.
    const code = (err as NodeJS.ErrnoException).code;
    if (code !== "ENOENT") throw err;
  }
}

/** Parse the contents of ~/.r1/daemon.json. Throws if missing. */
export async function readDaemonJson(_app: DesktopApp): Promise<DaemonJsonInfo> {
  const raw = await fs.readFile(daemonJsonPath(), "utf8");
  const parsed = JSON.parse(raw) as DaemonJsonInfo;
  return parsed;
}

/** Write a daemon.json file the desktop will discover on launch. */
export async function writeDaemonJson(info: DaemonJsonInfo): Promise<void> {
  const p = daemonJsonPath();
  await fs.mkdir(path.dirname(p), { recursive: true });
  await fs.writeFile(p, JSON.stringify(info, null, 2), "utf8");
}

// -------------------------------------------------------------------
// PID lifecycle
// -------------------------------------------------------------------

/** Send SIGTERM (and SIGKILL on a 1-s grace) to the named PID. */
export async function killSidecarPid(pid?: number): Promise<void> {
  if (typeof pid !== "number") {
    throw new Error("killSidecarPid: pid is required");
  }
  try {
    process.kill(pid, "SIGTERM");
  } catch {
    return; // already gone
  }
  await new Promise<void>((resolve) => setTimeout(resolve, 1000));
  try {
    process.kill(pid, "SIGKILL");
  } catch {
    // already gone after SIGTERM
  }
}

// -------------------------------------------------------------------
// Banner-state polling
// -------------------------------------------------------------------

interface ExpectBannerOpts {
  timeoutMs: number;
}

/** Poll the desktop's DaemonStatus pill until its state matches the
 *  expected `kind`. Returns the resolved BannerState. */
export async function expectBannerState(
  app: DesktopApp,
  kind: BannerState["kind"],
  opts: ExpectBannerOpts,
): Promise<BannerState> {
  const deadline = Date.now() + opts.timeoutMs;
  // The desktop exposes its state under "test://daemon-status" via
  // a debug-build hook in main.ts; in CI that hook routes through
  // tauri-driver's window.executeScript.
  while (Date.now() < deadline) {
    const state = await app.testState<BannerState>();
    if (state && state.kind === kind) return state;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(
    `expectBannerState: never reached "${kind}" within ${opts.timeoutMs} ms`,
  );
}

// -------------------------------------------------------------------
// App-launch — driver glue
// -------------------------------------------------------------------

/** Launch the desktop binary under tauri-driver and return a
 *  Page-like handle. The actual driver boot lives in the
 *  tauri-driver shim packaged with the e2e job; this fixture is
 *  the contract every spec talks to. */
export async function launchDesktopApp(
  opts: LaunchOptions = {},
): Promise<DesktopApp> {
  const session = await import("./tauri-driver-session");
  return session.launch(opts);
}
