// SPDX-License-Identifier: MIT
// useWorkdir — File System Access API hook with IndexedDB
// persistence and a manual-path fallback. Spec items 19/55 + 37/55.
//
// IMPORTANT (spec §Boundaries): we MUST NOT persist FSA handles in
// localStorage — handles are not serializable. We use IndexedDB
// instead. When FSA is unavailable (Firefox / Safari) the hook
// surfaces a flag and the WorkdirPickerDialog falls back to a manual
// path entry with autocomplete from `r1d.listAllowedRoots()`.
import { useCallback, useEffect, useRef, useState } from "react";

const DB_NAME = "r1-workdir";
const STORE = "handles";
const DB_VERSION = 1;

// Minimal portion of the FSA API we use. Avoid pulling in lib types
// to keep `lib` slim across bundles.
interface FsaDirectoryHandle {
  name: string;
  kind: "directory";
  queryPermission?: (options?: { mode: "read" | "readwrite" }) => Promise<"granted" | "denied" | "prompt">;
  requestPermission?: (options?: { mode: "read" | "readwrite" }) => Promise<"granted" | "denied" | "prompt">;
}

declare global {
  interface Window {
    showDirectoryPicker?: (options?: {
      mode?: "read" | "readwrite";
      startIn?: FsaDirectoryHandle | "desktop" | "documents" | "downloads" | "music" | "pictures" | "videos";
    }) => Promise<FsaDirectoryHandle>;
  }
}

export interface UseWorkdirState {
  /** True if the runtime supports the FSA API. */
  fsaSupported: boolean;
  /** The latest selected handle, or null if none / FSA unsupported. */
  handle: FsaDirectoryHandle | null;
  /** The basename of the chosen directory, for header display. */
  basename: string | null;
  /** Manual path fallback (Firefox / Safari, or when permission revoked). */
  manualPath: string | null;
  /** Latest error string from picker / permission flows. */
  error: string | null;
  /** Loading flag while IndexedDB / picker resolves. */
  loading: boolean;
}

export interface UseWorkdirResult extends UseWorkdirState {
  /** Open the native directory picker. Persists handle on success. */
  pickDirectory: () => Promise<void>;
  /** Use the manual path fallback. Persists path string in IDB. */
  setManualPath: (path: string) => Promise<void>;
  /** Clear the saved selection. */
  clear: () => Promise<void>;
  /** Re-prompt for permission on a previously-saved handle. */
  reauthorize: () => Promise<void>;
}

export interface UseWorkdirOptions {
  /** Logical key the workdir is stored under (e.g. sessionId). */
  storageKey: string;
  /** Test injection. */
  indexedDBImpl?: IDBFactory;
  /** Test injection: override `window.showDirectoryPicker`. */
  showDirectoryPicker?: Window["showDirectoryPicker"];
}

export function useWorkdir(opts: UseWorkdirOptions): UseWorkdirResult {
  const { storageKey } = opts;
  const idbRef = useRef<IDBFactory | undefined>(opts.indexedDBImpl ?? globalThis.indexedDB);
  const pickerRef = useRef<Window["showDirectoryPicker"] | undefined>(
    opts.showDirectoryPicker ?? (typeof window !== "undefined" ? window.showDirectoryPicker : undefined),
  );

  const [state, setState] = useState<UseWorkdirState>({
    fsaSupported: pickerRef.current !== undefined,
    handle: null,
    basename: null,
    manualPath: null,
    error: null,
    loading: true,
  });

  // Hydrate on mount.
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const stored = await idbGet(idbRef.current, storageKey);
        if (cancelled) return;
        if (!stored) {
          setState((prev) => ({ ...prev, loading: false }));
          return;
        }
        if (stored.kind === "handle") {
          setState((prev) => ({
            ...prev,
            handle: stored.handle,
            basename: stored.handle.name,
            manualPath: null,
            loading: false,
          }));
        } else {
          setState((prev) => ({
            ...prev,
            handle: null,
            basename: null,
            manualPath: stored.path,
            loading: false,
          }));
        }
      } catch (err) {
        if (cancelled) return;
        setState((prev) => ({
          ...prev,
          error: (err as Error)?.message ?? String(err),
          loading: false,
        }));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [storageKey]);

  const pickDirectory = useCallback(async (): Promise<void> => {
    if (!pickerRef.current) {
      setState((prev) => ({ ...prev, error: "FSA not supported", fsaSupported: false }));
      return;
    }
    try {
      const h = await pickerRef.current({ mode: "read" });
      await idbPut(idbRef.current, storageKey, { kind: "handle", handle: h });
      setState((prev) => ({
        ...prev,
        handle: h,
        basename: h.name,
        manualPath: null,
        error: null,
        loading: false,
      }));
    } catch (err) {
      setState((prev) => ({
        ...prev,
        error: (err as Error)?.message ?? String(err),
      }));
    }
  }, [storageKey]);

  const setManualPath = useCallback(async (path: string): Promise<void> => {
    try {
      await idbPut(idbRef.current, storageKey, { kind: "path", path });
      setState((prev) => ({
        ...prev,
        manualPath: path,
        handle: null,
        basename: pathBasename(path),
        error: null,
        loading: false,
      }));
    } catch (err) {
      setState((prev) => ({
        ...prev,
        error: (err as Error)?.message ?? String(err),
      }));
    }
  }, [storageKey]);

  const clear = useCallback(async (): Promise<void> => {
    try {
      await idbDel(idbRef.current, storageKey);
      setState((prev) => ({
        ...prev,
        handle: null,
        manualPath: null,
        basename: null,
        error: null,
      }));
    } catch (err) {
      setState((prev) => ({
        ...prev,
        error: (err as Error)?.message ?? String(err),
      }));
    }
  }, [storageKey]);

  const reauthorize = useCallback(async (): Promise<void> => {
    setState((prev) => {
      const h = prev.handle;
      if (!h?.requestPermission) {
        return { ...prev, error: "Cannot reauthorize: handle missing or permission API absent" };
      }
      void h.requestPermission({ mode: "read" }).catch((err) =>
        setState((p) => ({ ...p, error: (err as Error)?.message ?? String(err) })),
      );
      return prev;
    });
  }, []);

  return {
    ...state,
    pickDirectory,
    setManualPath,
    clear,
    reauthorize,
  };
}

function pathBasename(p: string): string | null {
  if (!p) return null;
  const trimmed = p.replace(/\/+$/, "");
  const idx = trimmed.lastIndexOf("/");
  return idx === -1 ? trimmed : trimmed.slice(idx + 1);
}

// ---------------------------------------------------------------------------
// IndexedDB helpers (no third-party dep).
// ---------------------------------------------------------------------------

type StoredEntry =
  | { kind: "handle"; handle: FsaDirectoryHandle }
  | { kind: "path"; path: string };

function openDb(idb: IDBFactory | undefined): Promise<IDBDatabase> {
  if (!idb) {
    return Promise.reject(new Error("IndexedDB unavailable"));
  }
  return new Promise((resolve, reject) => {
    const req = idb.open(DB_NAME, DB_VERSION);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(STORE)) {
        db.createObjectStore(STORE);
      }
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

async function idbGet(idb: IDBFactory | undefined, key: string): Promise<StoredEntry | undefined> {
  const db = await openDb(idb);
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, "readonly");
    const req = tx.objectStore(STORE).get(key);
    req.onsuccess = () => resolve(req.result as StoredEntry | undefined);
    req.onerror = () => reject(req.error);
  });
}

async function idbPut(idb: IDBFactory | undefined, key: string, entry: StoredEntry): Promise<void> {
  const db = await openDb(idb);
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, "readwrite");
    tx.objectStore(STORE).put(entry, key);
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

async function idbDel(idb: IDBFactory | undefined, key: string): Promise<void> {
  const db = await openDb(idb);
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, "readwrite");
    tx.objectStore(STORE).delete(key);
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}
