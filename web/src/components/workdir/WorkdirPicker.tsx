// SPDX-License-Identifier: MIT
// <WorkdirBadge> + <WorkdirPickerDialog> — workdir affordance. Spec item 37/55.
//
// The badge surfaces the current session's workdir as a button that
// opens the picker dialog. The dialog offers two paths:
//
//   1. **File System Access API**: when `window.showDirectoryPicker`
//      is available (Chromium-based browsers), the user can pick a
//      directory through the OS picker. We persist the resulting
//      `FileSystemDirectoryHandle` into IndexedDB keyed by daemon id
//      so the choice survives reloads (Chromium spec'd handles to be
//      structured-cloneable into IDB).
//
//   2. **Manual path entry**: a text input with a datalist suggestion
//      list pre-populated from `listAllowedRoots()` (typically wired
//      to `r1d.listAllowedRoots()`). Used by Firefox/Safari and as the
//      fallback when the user prefers typing.
//
// The dialog is controlled by the caller (open/onOpenChange) so the
// badge's testable surface is the button + label only, and the
// dialog's testable surface is the form + onSelect callback.
import { useEffect, useState } from "react";
import type { ReactElement } from "react";
import { FolderOpen } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

// ---------------------------------------------------------------------------
// IndexedDB persistence for FileSystemDirectoryHandle
// ---------------------------------------------------------------------------

const IDB_NAME = "r1-web";
const IDB_STORE = "workdirs";

interface IdbLike {
  open(name: string, version: number): IDBOpenDBRequest;
}

function getIdb(): IdbLike | null {
  if (typeof indexedDB === "undefined") return null;
  return indexedDB as unknown as IdbLike;
}

function openDb(): Promise<IDBDatabase | null> {
  const idb = getIdb();
  if (!idb) return Promise.resolve(null);
  return new Promise((resolve, reject) => {
    const req = idb.open(IDB_NAME, 1);
    req.onupgradeneeded = (): void => {
      const db = req.result as IDBDatabase;
      if (!db.objectStoreNames.contains(IDB_STORE)) {
        db.createObjectStore(IDB_STORE);
      }
    };
    req.onsuccess = (): void => resolve(req.result as IDBDatabase);
    req.onerror = (): void => reject(req.error);
  });
}

export async function persistWorkdirHandle(
  daemonId: string,
  handle: FileSystemDirectoryHandle | null,
): Promise<void> {
  const db = await openDb();
  if (!db) return;
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction(IDB_STORE, "readwrite");
    if (handle === null) {
      tx.objectStore(IDB_STORE).delete(daemonId);
    } else {
      tx.objectStore(IDB_STORE).put(handle, daemonId);
    }
    tx.oncomplete = (): void => resolve();
    tx.onerror = (): void => reject(tx.error);
  });
  db.close();
}

export async function loadWorkdirHandle(
  daemonId: string,
): Promise<FileSystemDirectoryHandle | null> {
  const db = await openDb();
  if (!db) return null;
  const handle = await new Promise<FileSystemDirectoryHandle | null>(
    (resolve, reject) => {
      const tx = db.transaction(IDB_STORE, "readonly");
      const req = tx.objectStore(IDB_STORE).get(daemonId);
      req.onsuccess = (): void => resolve((req.result as FileSystemDirectoryHandle) ?? null);
      req.onerror = (): void => reject(req.error);
    },
  );
  db.close();
  return handle;
}

// ---------------------------------------------------------------------------
// Badge
// ---------------------------------------------------------------------------

export interface WorkdirBadgeProps {
  workdir: string | null;
  onOpenPicker: () => void;
  /** Optional override label (defaults to workdir or "no workdir"). */
  label?: string;
}

export function WorkdirBadge({
  workdir,
  onOpenPicker,
  label,
}: WorkdirBadgeProps): ReactElement {
  const display = label ?? workdir ?? "no workdir";
  return (
    <button
      type="button"
      onClick={onOpenPicker}
      data-testid="workdir-badge"
      aria-label={`Change workdir, current: ${workdir ?? "none"}`}
      className="inline-flex items-center gap-1 px-2 py-0.5 text-xs rounded-md border border-border hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <FolderOpen className="w-3 h-3" aria-hidden="true" />
      <span className="font-mono truncate max-w-[28ch]">{display}</span>
    </button>
  );
}

// ---------------------------------------------------------------------------
// Dialog
// ---------------------------------------------------------------------------

export interface WorkdirPickerDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Callback fired when the user confirms a workdir selection. The
   *  string is the path to send to the daemon; the handle (when
   *  present) is the FSA handle for IndexedDB persistence. */
  onSelect: (
    path: string,
    handle: FileSystemDirectoryHandle | null,
  ) => Promise<void> | void;
  /** Initial value for the manual-entry input. */
  defaultPath?: string;
  /** Resolves to the daemon's allowlist for autocomplete. */
  listAllowedRoots: () => Promise<string[]>;
  /** Test injection: replaces window.showDirectoryPicker. */
  showDirectoryPicker?: () => Promise<FileSystemDirectoryHandle>;
}

function fsaAvailable(
  injected?: () => Promise<FileSystemDirectoryHandle>,
): boolean {
  if (injected) return true;
  return (
    typeof window !== "undefined" &&
    typeof (window as unknown as { showDirectoryPicker?: unknown })
      .showDirectoryPicker === "function"
  );
}

export function WorkdirPickerDialog({
  open,
  onOpenChange,
  onSelect,
  defaultPath = "",
  listAllowedRoots,
  showDirectoryPicker,
}: WorkdirPickerDialogProps): ReactElement {
  const [path, setPath] = useState<string>(defaultPath);
  const [roots, setRoots] = useState<string[]>([]);
  const [loadingFsa, setLoadingFsa] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setPath(defaultPath);
    setError(null);
    let cancelled = false;
    listAllowedRoots()
      .then((r) => {
        if (!cancelled) setRoots(r);
      })
      .catch(() => {
        if (!cancelled) setRoots([]);
      });
    return () => {
      cancelled = true;
    };
  }, [open, defaultPath, listAllowedRoots]);

  const onPickViaFsa = async (): Promise<void> => {
    setError(null);
    setLoadingFsa(true);
    try {
      const fn =
        showDirectoryPicker ??
        (window as unknown as {
          showDirectoryPicker: () => Promise<FileSystemDirectoryHandle>;
        }).showDirectoryPicker;
      const handle = await fn();
      // FSA handle.name is the leaf directory name; we still want a full
      // path. The user must confirm a path string in the input below
      // before submitting (we pre-fill with the leaf so they have a
      // sensible starting point).
      setPath((p) => (p && p.length > 0 ? p : handle.name));
      await onSelect(path || handle.name, handle);
      onOpenChange(false);
    } catch (e) {
      const message = e instanceof Error ? e.message : "picker cancelled";
      setError(message);
    } finally {
      setLoadingFsa(false);
    }
  };

  const onSubmitManual = async (): Promise<void> => {
    const trimmed = path.trim();
    if (trimmed.length === 0) {
      setError("Workdir is required");
      return;
    }
    await onSelect(trimmed, null);
    onOpenChange(false);
  };

  const fsaOk = fsaAvailable(showDirectoryPicker);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        data-testid="workdir-picker-dialog"
        aria-label="Pick a workdir"
      >
        <DialogHeader>
          <DialogTitle>Pick a workdir</DialogTitle>
          <DialogDescription>
            Choose a directory the daemon is allowed to open. Selections are
            cached so reloading the page picks up where you left off.
          </DialogDescription>
        </DialogHeader>

        {fsaOk ? (
          <div className="space-y-2">
            <Button
              type="button"
              onClick={onPickViaFsa}
              disabled={loadingFsa}
              data-testid="workdir-picker-fsa"
              aria-label="Open directory picker"
            >
              {loadingFsa ? "Opening picker…" : "Choose directory…"}
            </Button>
            <p className="text-xs text-muted-foreground">
              Uses the browser&apos;s native directory picker. The selection
              is persisted via IndexedDB.
            </p>
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">
            Browser does not support the File System Access API; enter the
            path manually below.
          </p>
        )}

        <form
          onSubmit={(e) => {
            e.preventDefault();
            void onSubmitManual();
          }}
          className="space-y-2"
          data-testid="workdir-picker-form"
        >
          <label htmlFor="workdir-picker-path" className="text-sm font-medium">
            Path
          </label>
          <Input
            id="workdir-picker-path"
            list="workdir-picker-roots"
            value={path}
            onChange={(e) => setPath(e.target.value)}
            data-testid="workdir-picker-path"
            aria-label="Workdir path"
            autoComplete="off"
            spellCheck={false}
          />
          <datalist id="workdir-picker-roots">
            {roots.map((r) => (
              <option key={r} value={r} />
            ))}
          </datalist>
          {error ? (
            <p
              role="alert"
              data-testid="workdir-picker-error"
              className="text-xs text-destructive"
            >
              {error}
            </p>
          ) : null}
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
              data-testid="workdir-picker-cancel"
              aria-label="Cancel workdir picker"
            >
              Cancel
            </Button>
            <Button
              type="submit"
              data-testid="workdir-picker-submit"
              aria-label="Use this workdir"
            >
              Use workdir
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
