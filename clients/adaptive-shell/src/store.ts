import {
  LAYOUT_SCHEMA_VERSION,
  emptyLayoutState,
  layoutKey,
  migrateLayoutState,
  sanitizeLayoutState,
  type DeviceLayoutState,
} from "./storage.js";
import { isUiDeviceClass, type UiDeviceClass } from "./device.js";

// The device-local layout store. One store instance serves the whole shell;
// reads migrate and revalidate stored bytes, and every write is one
// IndexedDB read-modify-write transaction whose mutator always re-applies
// onto the freshest committed record. Two tabs (or two stale closures)
// writing the same key therefore serialize, and the loser rebases onto the
// winner instead of silently clobbering — the record's revision/updated_at
// make every such adjudication observable.
export interface LayoutStore {
  load(deviceClass: UiDeviceClass, projectId: string, now: string): Promise<DeviceLayoutState>;
  update(
    deviceClass: UiDeviceClass,
    projectId: string,
    now: string,
    mutate: (state: DeviceLayoutState) => DeviceLayoutState,
  ): Promise<DeviceLayoutState>;
  // removeProject drops every device-class record of one project
  // (archive/uninstall sweeps).
  removeProject(projectId: string): Promise<void>;
  // sweep drops records whose project is not in the valid set (server-truth
  // drift after archive or a logout/login on the same browser profile).
  sweep(validProjectIds: ReadonlySet<string>): Promise<void>;
  // pruneAppInstance removes every reference to one app instance (uninstall
  // sweep) across all stored records.
  pruneAppInstance(appInstanceId: string): Promise<void>;
  // clearAll drops every record (logout sweeps this browser profile's
  // shell state).
  clearAll(): Promise<void>;
}

const DATABASE_NAME = "workos-adaptive-shell";
const DATABASE_VERSION = 1;
const STORE_NAME = "layout-states";

export function createLayoutStore(idbFactory?: IDBFactory): LayoutStore {
  const factory = idbFactory ?? (typeof indexedDB === "undefined" ? undefined : indexedDB);
  if (!factory) return createMemoryLayoutStore();
  return new IndexedDbLayoutStore(factory);
}

// The IndexedDB adapter. Opening failures (private modes, quota, disabled
// storage) degrade permanently to the in-memory store: the shell keeps
// working with session-only layout memory rather than breaking.
class IndexedDbLayoutStore implements LayoutStore {
  #database: Promise<IDBDatabase | undefined>;

  constructor(factory: IDBFactory) {
    this.#database = new Promise((resolve) => {
      let request: IDBOpenDBRequest;
      try {
        request = factory.open(DATABASE_NAME, DATABASE_VERSION);
      } catch {
        resolve(undefined);
        return;
      }
      request.onupgradeneeded = () => {
        const database = request.result;
        if (!database.objectStoreNames.contains(STORE_NAME)) {
          database.createObjectStore(STORE_NAME);
        }
      };
      request.onsuccess = () => {
        resolve(request.result);
      };
      request.onerror = () => {
        resolve(undefined);
      };
      request.onblocked = () => {
        resolve(undefined);
      };
    });
  }

  // awaitTransaction settles when the transaction commits or aborts.
  #awaitTransaction<T>(transaction: IDBTransaction, result: T): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      transaction.oncomplete = () => {
        resolve(result);
      };
      transaction.onabort = () => {
        reject(transaction.error ?? new Error("layout store transaction aborted"));
      };
      transaction.onerror = () => {
        reject(transaction.error ?? new Error("layout store transaction failed"));
      };
    });
  }

  async load(
    deviceClass: UiDeviceClass,
    projectId: string,
    now: string,
  ): Promise<DeviceLayoutState> {
    const opened = await this.#database;
    if (!opened) return emptyLayoutState(projectId, deviceClass, now);
    const key = layoutKey(deviceClass, projectId);
    return new Promise<DeviceLayoutState>((resolve) => {
      let transaction: IDBTransaction;
      try {
        transaction = opened.transaction(STORE_NAME, "readonly");
      } catch {
        resolve(emptyLayoutState(projectId, deviceClass, now));
        return;
      }
      const request = transaction.objectStore(STORE_NAME).get(key);
      request.onsuccess = () => {
        resolve(migrateLayoutState(request.result, projectId, deviceClass, now));
      };
      request.onerror = () => {
        // A failed read resets exactly this key, never the whole store.
        resolve(emptyLayoutState(projectId, deviceClass, now));
      };
    });
  }

  async update(
    deviceClass: UiDeviceClass,
    projectId: string,
    now: string,
    mutate: (state: DeviceLayoutState) => DeviceLayoutState,
  ): Promise<DeviceLayoutState> {
    const opened = await this.#database;
    if (!opened) {
      // No durable storage: honor the mutation against the empty record so
      // callers keep a consistent in-memory state for this session.
      return {
        ...mutate(emptyLayoutState(projectId, deviceClass, now)),
        updatedAt: now,
        revision: 1,
      };
    }
    const key = layoutKey(deviceClass, projectId);
    return new Promise<DeviceLayoutState>((resolve, reject) => {
      let transaction: IDBTransaction;
      try {
        transaction = opened.transaction(STORE_NAME, "readwrite");
      } catch (error) {
        reject(error instanceof Error ? error : new Error("layout store transaction failed"));
        return;
      }
      const store = transaction.objectStore(STORE_NAME);
      const getRequest = store.get(key);
      let committed = false;
      getRequest.onsuccess = () => {
        try {
          // The mutator runs inside the same transaction, based on the
          // committed record: overlapping readwrite transactions on this
          // store serialize, so this is the multi-tab adjudication point.
          const current = migrateLayoutState(getRequest.result, projectId, deviceClass, now);
          const mutated = mutate(current);
          const record: DeviceLayoutState = {
            ...mutated,
            schemaVersion: LAYOUT_SCHEMA_VERSION,
            projectId,
            deviceClass,
            updatedAt: now,
            revision: current.revision + 1,
          };
          store.put(record, key);
          committed = true;
          transaction.oncomplete = () => {
            resolve(record);
          };
        } catch (error) {
          transaction.abort();
          reject(error instanceof Error ? error : new Error("layout store mutation failed"));
        }
      };
      getRequest.onerror = () => {
        reject(getRequest.error ?? new Error("layout store read failed"));
      };
      transaction.onabort = () => {
        if (!committed) {
          reject(transaction.error ?? new Error("layout store transaction aborted"));
        }
      };
    });
  }

  async removeProject(projectId: string): Promise<void> {
    await this.#transformAll((key, record) =>
      key.endsWith(`/${projectId}`) || record.projectId === projectId ? undefined : record,
    );
  }

  async sweep(validProjectIds: ReadonlySet<string>): Promise<void> {
    await this.#transformAll((_key, record) =>
      validProjectIds.has(record.projectId) ? record : undefined,
    );
  }

  async pruneAppInstance(appInstanceId: string): Promise<void> {
    await this.#transformAll((_key, record) => withoutAppInstance(record, appInstanceId));
  }

  // transformAll walks every stored record inside one readwrite cursor
  // transaction. The visitor returns the (re-sanitized) record to keep, or
  // undefined to delete it. Bytes that fail sanitization are always
  // deleted: corruption resets exactly the offending key, never the store.
  async #transformAll(
    visitor: (key: string, record: DeviceLayoutState) => DeviceLayoutState | undefined,
  ): Promise<void> {
    const opened = await this.#database;
    if (!opened) return;
    await new Promise<void>((resolve, reject) => {
      let transaction: IDBTransaction;
      try {
        transaction = opened.transaction(STORE_NAME, "readwrite");
      } catch (error) {
        reject(error instanceof Error ? error : new Error("layout store transaction failed"));
        return;
      }
      const cursorRequest = transaction.objectStore(STORE_NAME).openCursor();
      cursorRequest.onsuccess = () => {
        const cursor = cursorRequest.result;
        if (!cursor) return;
        const key = cursor.key;
        try {
          if (typeof key !== "string") {
            cursor.delete();
          } else {
            const parts = key.split("/");
            const deviceClass = parts[1];
            const projectId = parts.slice(2).join("/");
            const sanitized =
              sanitizeLayoutState(
                cursor.value,
                projectId,
                isUiDeviceClass(deviceClass) ? deviceClass : "desktop",
              ) ?? undefined;
            if (!sanitized) {
              cursor.delete();
            } else {
              const next = visitor(key, sanitized);
              if (next === undefined) {
                cursor.delete();
              } else if (next !== sanitized) {
                cursor.update(next);
              }
            }
          }
        } catch {
          cursor.delete();
        }
        cursor.continue();
      };
      cursorRequest.onerror = () => {
        reject(cursorRequest.error ?? new Error("layout store sweep failed"));
      };
      transaction.oncomplete = () => {
        resolve();
      };
    });
  }

  async clearAll(): Promise<void> {
    const opened = await this.#database;
    if (!opened) return;
    const transaction = opened.transaction(STORE_NAME, "readwrite");
    transaction.objectStore(STORE_NAME).clear();
    await this.#awaitTransaction(transaction, undefined);
  }
}

// withoutAppInstance drops every reference to one app instance from a
// record. Artifact references are artifact identities, not instance
// identities, so they stay untouched.
function withoutAppInstance(
  state: DeviceLayoutState,
  appInstanceId: string,
): DeviceLayoutState | undefined {
  const keep = (ids: string[]) => ids.filter((id) => id !== appInstanceId);
  const recentAppInstanceIds = keep(state.recentAppInstanceIds);
  const dockAppInstanceIds = keep(state.dockAppInstanceIds);
  const activeAppInstanceId =
    state.activeAppInstanceId === appInstanceId ? undefined : state.activeAppInstanceId;
  if (
    recentAppInstanceIds.length === state.recentAppInstanceIds.length &&
    dockAppInstanceIds.length === state.dockAppInstanceIds.length &&
    activeAppInstanceId === state.activeAppInstanceId
  ) {
    return state;
  }
  return { ...state, activeAppInstanceId, recentAppInstanceIds, dockAppInstanceIds };
}

// The in-memory store: deterministic fallback for environments without
// IndexedDB and the fixture store for unit tests. Updates run through a
// promise-chain mutex so concurrent writers observe the same
// read-modify-write serialization the IndexedDB adapter gets from the
// transaction scheduler.
export function createMemoryLayoutStore(): LayoutStore {
  const records = new Map<string, DeviceLayoutState>();
  let tail: Promise<unknown> = Promise.resolve();
  const serialized = <T>(operation: () => T): Promise<T> => {
    const run = tail.then(operation, operation);
    tail = run.catch(() => undefined);
    return run;
  };
  return {
    load(deviceClass, projectId, now) {
      return Promise.resolve(
        records.get(layoutKey(deviceClass, projectId)) ??
          emptyLayoutState(projectId, deviceClass, now),
      );
    },
    update(deviceClass, projectId, now, mutate) {
      return serialized(() => {
        const key = layoutKey(deviceClass, projectId);
        const current = records.get(key) ?? emptyLayoutState(projectId, deviceClass, now);
        const mutated = mutate(current);
        const next: DeviceLayoutState = {
          ...mutated,
          schemaVersion: LAYOUT_SCHEMA_VERSION,
          projectId,
          deviceClass,
          updatedAt: now,
          revision: current.revision + 1,
        };
        records.set(key, next);
        return next;
      });
    },
    removeProject(projectId) {
      return serialized(() => {
        for (const key of [...records.keys()]) {
          if (key.endsWith(`/${projectId}`)) records.delete(key);
        }
      });
    },
    sweep(validProjectIds) {
      return serialized(() => {
        for (const [key, record] of [...records.entries()]) {
          if (!validProjectIds.has(record.projectId)) records.delete(key);
        }
      });
    },
    pruneAppInstance(appInstanceId) {
      return serialized(() => {
        for (const [key, record] of [...records.entries()]) {
          const next = withoutAppInstance(record, appInstanceId);
          if (next === undefined) records.delete(key);
          else records.set(key, next);
        }
      });
    },
    clearAll() {
      return serialized(() => {
        records.clear();
      });
    },
  };
}
