/** Minimal storage contract used to persist the installation id. Matches `localStorage`. */
export interface KeyValueStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export function createMemoryStorage(): KeyValueStorage {
  const map = new Map<string, string>();
  return {
    getItem: (key) => map.get(key) ?? null,
    setItem: (key, value) => {
      map.set(key, value);
    },
  };
}

/**
 * `localStorage` when the runtime exposes it (accessing it can throw in sandboxed
 * iframes or with storage disabled), otherwise an in-memory store.
 */
export function defaultStorage(): KeyValueStorage {
  try {
    const ls = (globalThis as { localStorage?: KeyValueStorage }).localStorage;
    if (ls && typeof ls.getItem === "function" && typeof ls.setItem === "function") return ls;
  } catch {
    // fall through to memory storage
  }
  return createMemoryStorage();
}
