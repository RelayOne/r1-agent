// SPDX-License-Identifier: MIT
// Tests for useWorkdir. We exercise the FSA + manual-path fallback
// + IndexedDB persistence. Since jsdom lacks a real IndexedDB, we
// inject a minimal in-memory IDBFactory that satisfies the surface
// the hook touches (open, transaction, objectStore, get/put/delete).
import React from "react";
import { describe, it, expect } from "vitest";
import { render, act } from "@testing-library/react";
import { useWorkdir, type UseWorkdirResult } from "@/hooks/useWorkdir";

// ---------------------------------------------------------------------------
// In-memory IDB mock
// ---------------------------------------------------------------------------
class FakeStore {
  data = new Map<string, unknown>();
  put(key: string, value: unknown): void { this.data.set(key, value); }
  get(key: string): unknown { return this.data.get(key); }
  delete(key: string): void { this.data.delete(key); }
}
class FakeRequest {
  result: unknown;
  error: unknown;
  onsuccess: (() => void) | null = null;
  onerror: (() => void) | null = null;
  resolve(value: unknown): void {
    this.result = value;
    queueMicrotask(() => this.onsuccess?.());
  }
}
class FakeTx {
  oncomplete: (() => void) | null = null;
  onerror: (() => void) | null = null;
  error: unknown = null;
  constructor(private store: FakeStore) {}
  objectStore(): {
    get(k: string): FakeRequest;
    put(v: unknown, k: string): void;
    delete(k: string): void;
  } {
    return {
      get: (k: string) => {
        const req = new FakeRequest();
        req.resolve(this.store.get(k));
        return req;
      },
      put: (v: unknown, k: string) => this.store.put(k, v),
      delete: (k: string) => this.store.delete(k),
    };
  }
  done(): void {
    queueMicrotask(() => this.oncomplete?.());
  }
}
class FakeDb {
  objectStoreNames = { contains: (_n: string) => true };
  constructor(private store: FakeStore) {}
  createObjectStore(): void { /* noop */ }
  transaction(_name: string, _mode?: string): FakeTx {
    const tx = new FakeTx(this.store);
    queueMicrotask(() => tx.done());
    return tx;
  }
}
function fakeIdbFactory(): IDBFactory {
  const store = new FakeStore();
  return {
    open(_name: string, _version?: number) {
      const req = new FakeRequest();
      const db = new FakeDb(store);
      req.resolve(db);
      return req as unknown as IDBOpenDBRequest;
    },
    deleteDatabase() { return new FakeRequest() as unknown as IDBOpenDBRequest; },
    cmp() { return 0; },
    databases: async () => [],
  } as unknown as IDBFactory;
}

interface HostProps { storageKey: string; idb: IDBFactory; picker?: typeof window.showDirectoryPicker; api: { current: UseWorkdirResult | null }; }
function Host({ storageKey, idb, picker, api }: HostProps): React.ReactElement {
  const r = useWorkdir({
    storageKey,
    indexedDBImpl: idb,
    ...(picker !== undefined && { showDirectoryPicker: picker }),
  });
  api.current = r;
  return <div />;
}

async function flushAsync(): Promise<void> {
  // Allow microtasks (request callbacks + setState) to settle.
  await new Promise((r) => setTimeout(r, 0));
  await new Promise((r) => setTimeout(r, 0));
}

describe("useWorkdir", () => {
  it("starts with loading=true then settles to no selection", async () => {
    const idb = fakeIdbFactory();
    const api: HostProps["api"] = { current: null };
    await act(async () => {
      render(<Host storageKey="s1" idb={idb} api={api} />);
      await flushAsync();
    });
    expect(api.current?.loading).toStrictEqual(false);
    expect(api.current?.handle).toBeNull();
    expect(api.current?.manualPath).toBeNull();
  });

  it("setManualPath persists and exposes basename", async () => {
    const idb = fakeIdbFactory();
    const api: HostProps["api"] = { current: null };
    await act(async () => {
      render(<Host storageKey="k1" idb={idb} api={api} />);
      await flushAsync();
    });
    await act(async () => {
      await api.current!.setManualPath("/home/user/project");
      await flushAsync();
    });
    expect(api.current?.manualPath).toBe("/home/user/project");
    expect(api.current?.basename).toBe("project");
  });

  it("clear() removes the saved selection", async () => {
    const idb = fakeIdbFactory();
    const api: HostProps["api"] = { current: null };
    await act(async () => {
      render(<Host storageKey="k1" idb={idb} api={api} />);
      await flushAsync();
    });
    await act(async () => {
      await api.current!.setManualPath("/foo/bar");
      await flushAsync();
    });
    expect(api.current?.manualPath).toBe("/foo/bar");
    await act(async () => {
      await api.current!.clear();
      await flushAsync();
    });
    expect(api.current?.manualPath).toBeNull();
    expect(api.current?.basename).toBeNull();
  });

  it("pickDirectory persists handle and surfaces basename", async () => {
    const idb = fakeIdbFactory();
    const fakeHandle = { name: "myrepo", kind: "directory" as const };
    const picker = (async () => fakeHandle) as unknown as typeof window.showDirectoryPicker;
    const api: HostProps["api"] = { current: null };
    await act(async () => {
      render(<Host storageKey="k2" idb={idb} picker={picker} api={api} />);
      await flushAsync();
    });
    await act(async () => {
      await api.current!.pickDirectory();
      await flushAsync();
    });
    expect(api.current?.basename).toBe("myrepo");
    expect(api.current?.handle?.name).toBe("myrepo");
  });

  it("flags fsaSupported=false when picker unavailable and surfaces error on pick", async () => {
    const idb = fakeIdbFactory();
    const api: HostProps["api"] = { current: null };
    await act(async () => {
      render(<Host storageKey="k3" idb={idb} picker={undefined} api={api} />);
      await flushAsync();
    });
    expect(api.current?.fsaSupported).toStrictEqual(false);
    await act(async () => {
      await api.current!.pickDirectory();
      await flushAsync();
    });
    expect(api.current?.error).toMatch(/FSA/);
  });
});
