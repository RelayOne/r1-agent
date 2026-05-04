// SPDX-License-Identifier: MIT
//
// Per-session metadata store backed by tauri-plugin-store.
//
// Implements spec desktop-cortex-augmentation §7. Persists each
// session's user-facing metadata (name, workdir, archive flag,
// pinned lane ids) to a single store file `sessions.json` under the
// app data dir (`~/Library/Application Support/dev.r1.desktop/` on
// macOS; equivalents on Linux/Windows). One key per session_id; value
// is a `SessionMeta` object.
//
// The folder picker `pickWorkdir` chains:
//   1. tauri-plugin-dialog `open()` for the OS picker
//   2. plugin-store mutation
//   3. invoke('session.set_workdir') so the daemon binds cmd.Dir for
//      that session.
//
// On Wayland environments where `open()` returns null twice in a row
// (xdg-desktop-portal not available — spec §12 R4), a fallback
// manual-path entry path is offered to the caller.
//
// Migration: if a previous build wrote `r1-session-<id>-workdir` to
// `localStorage`, the first read for that session copies the value
// into the store and deletes the localStorage key.

import { invoke } from "@tauri-apps/api/core";
import { open as openDialog } from "@tauri-apps/plugin-dialog";
import { load, type Store } from "@tauri-apps/plugin-store";

// -------------------------------------------------------------------
// Types (spec §7)
// -------------------------------------------------------------------

export interface SessionMeta {
  id: string; // ULID
  name: string;
  workdir: string | null;
  workdir_set_at?: string;
  archived: boolean;
  created_at: string;
  last_used_at: string;
  pinned_lane_ids: string[];
}

const STORE_FILE = "sessions.json";

// -------------------------------------------------------------------
// Defaults + migration helpers
// -------------------------------------------------------------------

export function defaultMeta(sessionId: string): SessionMeta {
  const nowIso = new Date().toISOString();
  return {
    id: sessionId,
    name: `Session ${shortId(sessionId)}`,
    workdir: null,
    archived: false,
    created_at: nowIso,
    last_used_at: nowIso,
    pinned_lane_ids: [],
  };
}

function shortId(sessionId: string): string {
  // Trim ULID to last 6 chars for human-readable default name.
  return sessionId.length > 6 ? sessionId.slice(-6) : sessionId;
}

const LEGACY_LS_PREFIX = "r1-session-";

/**
 * If a previous build stored a workdir under
 * `localStorage["r1-session-<id>-workdir"]`, copy it into `meta` and
 * delete the legacy key. Returns the (possibly mutated) meta.
 */
function migrateFromLocalStorage(meta: SessionMeta): SessionMeta {
  if (typeof globalThis.localStorage === "undefined") return meta;
  const key = `${LEGACY_LS_PREFIX}${meta.id}-workdir`;
  const legacy = globalThis.localStorage.getItem(key);
  if (legacy && !meta.workdir) {
    meta.workdir = legacy;
    meta.workdir_set_at = new Date().toISOString();
  }
  if (legacy !== null) {
    globalThis.localStorage.removeItem(key);
  }
  return meta;
}

// -------------------------------------------------------------------
// Store handle (lazy)
// -------------------------------------------------------------------

let storePromise: Promise<Store> | null = null;

async function storeHandle(): Promise<Store> {
  if (!storePromise) {
    storePromise = load(STORE_FILE, { autoSave: true });
  }
  return storePromise;
}

/**
 * Test-only: reset the cached store handle so unit tests can swap a
 * mock plugin-store between cases. Production code never calls this.
 */
export function __resetForTests(): void {
  storePromise = null;
}

// -------------------------------------------------------------------
// CRUD surface (spec §7)
// -------------------------------------------------------------------

/**
 * Read the persisted meta for one session. Lazily creates a default
 * meta + migrates from legacy localStorage if needed; the returned
 * object is always a fresh shallow copy so callers can mutate without
 * corrupting cached state.
 */
export async function getMeta(sessionId: string): Promise<SessionMeta> {
  const store = await storeHandle();
  const raw = (await store.get<SessionMeta>(sessionId)) ?? null;
  let meta: SessionMeta = raw ? { ...raw } : defaultMeta(sessionId);
  // Defensive: the stored shape evolves; fill missing fields.
  if (!Array.isArray(meta.pinned_lane_ids)) meta.pinned_lane_ids = [];
  if (typeof meta.archived !== "boolean") meta.archived = false;
  meta = migrateFromLocalStorage(meta);
  return meta;
}

/**
 * Persist a meta. Always bumps `last_used_at` so the sidebar can
 * order by recency without exposing this concern to call-sites.
 */
export async function setMeta(meta: SessionMeta): Promise<void> {
  const store = await storeHandle();
  const next: SessionMeta = {
    ...meta,
    last_used_at: new Date().toISOString(),
  };
  await store.set(meta.id, next);
}

/**
 * Mark a session as archived. Archived sessions remain in storage
 * (so re-opening them is a one-click affair) but are filtered out of
 * the active sidebar by `listAll({ archived: false })`.
 */
export async function archive(sessionId: string): Promise<void> {
  const meta = await getMeta(sessionId);
  meta.archived = true;
  await setMeta(meta);
}

/**
 * Inverse of `archive`. Surfaces a previously-archived session.
 */
export async function unarchive(sessionId: string): Promise<void> {
  const meta = await getMeta(sessionId);
  meta.archived = false;
  await setMeta(meta);
}

/**
 * List every session_id known to the store. Optionally filter by
 * archive flag.
 */
export async function listAll(opts?: {
  archived?: boolean;
}): Promise<SessionMeta[]> {
  const store = await storeHandle();
  const keys = await store.keys();
  const out: SessionMeta[] = [];
  for (const k of keys) {
    const v = await store.get<SessionMeta>(k);
    if (!v) continue;
    if (opts?.archived !== undefined && v.archived !== opts.archived) continue;
    out.push(v);
  }
  return out;
}

/**
 * Clear the workdir on a session. Does NOT call
 * `session.set_workdir` because the daemon's set_workdir verb
 * doesn't accept null — the desktop simply forgets the binding so
 * the next daemon-side spawn falls back to its default.
 */
export async function clearWorkdir(sessionId: string): Promise<void> {
  const meta = await getMeta(sessionId);
  meta.workdir = null;
  meta.workdir_set_at = undefined;
  await setMeta(meta);
}

// -------------------------------------------------------------------
// Folder picker (spec §7)
// -------------------------------------------------------------------

export interface PickWorkdirOptions {
  /** Title shown above the OS picker dialog. */
  title?: string;
  /**
   * Wayland fallback callback. Called when `openDialog()` returns null
   * twice in a row, which on Linux/Wayland indicates xdg-desktop-portal
   * is not available (spec §12 R4). Receives the title; should return
   * a manually-typed absolute path or null to abort.
   */
  fallbackInput?: (title: string) => Promise<string | null>;
}

let consecutiveDialogNulls = 0;

/**
 * Open the OS folder picker, persist the result on the named session,
 * and notify the daemon via `session.set_workdir`. Returns the chosen
 * path or null if the user cancelled (and the fallback also yielded
 * nothing).
 *
 * The daemon-side handler MAY return `conflict` if the session has
 * any in-flight tool calls (per spec §7); in that case the error
 * surfaces through the underlying invoke() and the caller (UI) should
 * render the modal prompting the user to wait or cancel.
 */
export async function pickWorkdir(
  sessionId: string,
  opts: PickWorkdirOptions = {},
): Promise<string | null> {
  const title = opts.title ?? "Pick session workspace";
  const picked = await openDialog({
    directory: true,
    multiple: false,
    title,
  });
  let path: string | null = typeof picked === "string" ? picked : null;
  if (!path) {
    consecutiveDialogNulls += 1;
    if (consecutiveDialogNulls >= 2 && opts.fallbackInput) {
      path = await opts.fallbackInput(title);
    }
  } else {
    consecutiveDialogNulls = 0;
  }
  if (!path) return null;

  const meta = await getMeta(sessionId);
  meta.workdir = path;
  meta.workdir_set_at = new Date().toISOString();
  await setMeta(meta);

  // Push to daemon so cmd.Dir-equivalent binds for any subprocess
  // it spawns on this session's behalf.
  await invoke("session_set_workdir", {
    params: { session_id: sessionId, workdir: path },
  });

  return path;
}

/** Test-only: reset the consecutive-null counter so each test starts fresh. */
export function __resetDialogCounterForTests(): void {
  consecutiveDialogNulls = 0;
}
