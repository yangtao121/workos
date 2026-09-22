import { fromJsonString, toJsonString } from "@bufbuild/protobuf";
import { SessionDirectiveSchema, type SessionDirective } from "@workos/protocol";

const PREFIX = "workos.session-continuity.v1/";
const DATABASE = "workos-session-continuity";
const STORE = "journals";
const EPOCH_KEY = "clear-epoch";
const MAX_RECORD_BYTES = 512 * 1024;
const MAX_CURSOR = (1n << 63n) - 1n;
export const MAX_DRAFT_LENGTH = 16_384;
let clearGeneration = 0;
let database: Promise<IDBDatabase | undefined> | undefined;
export interface PendingSessionInput {
  clientInputId: string;
  text: string;
  directive?: SessionDirective | undefined;
  phase: "submitting" | "recovering" | "failed";
  error?: string;
}
export interface SessionContinuity {
  draft: string;
  pending: PendingSessionInput[];
  cursor: bigint;
  epoch?: string;
}
export interface ContinuityPatch {
  draft?: string;
  clearDraftIf?: string;
  addPending?: PendingSessionInput[];
  removePendingIds?: string[];
  cursor?: bigint;
  resetCursor?: bigint;
}
export interface ContinuityGuard {
  epoch: string;
  generation: number;
}
export function sessionContinuityGeneration(): number {
  return clearGeneration;
}
export function sessionContinuityKey(owner: string, project: string, session: string): string {
  return owner && project && session
    ? PREFIX + [owner, project, session].map(encodeURIComponent).join("/")
    : "";
}
function open(): Promise<IDBDatabase | undefined> {
  database ??= new Promise((resolve) => {
    try {
      const request = indexedDB.open(DATABASE, 1);
      request.onupgradeneeded = () => {
        request.result.createObjectStore(STORE);
      };
      request.onsuccess = () => {
        const db = request.result;
        db.onversionchange = () => {
          db.close();
          database = undefined;
        };
        resolve(db);
      };
      request.onerror = () => {
        database = undefined;
        resolve(undefined);
      };
      request.onblocked = () => {
        database = undefined;
        resolve(undefined);
      };
    } catch {
      database = undefined;
      resolve(undefined);
    }
  });
  return database;
}
function decode(raw: unknown): SessionContinuity {
  const empty = { draft: "", pending: [], cursor: 0n };
  try {
    if (typeof raw !== "string" || raw.length > MAX_RECORD_BYTES) return empty;
    const value = JSON.parse(raw) as {
      draft?: unknown;
      pending?: { clientInputId?: unknown; text?: unknown; directive?: unknown }[];
      cursor?: unknown;
    };
    if (
      typeof value.draft !== "string" ||
      value.draft.length > MAX_DRAFT_LENGTH ||
      !Array.isArray(value.pending) ||
      value.pending.length > 16
    )
      return empty;
    const pending = value.pending.map((item): PendingSessionInput => {
      if (
        typeof item.clientInputId !== "string" ||
        !item.clientInputId ||
        item.clientInputId.length > 128 ||
        typeof item.text !== "string" ||
        item.text.length > MAX_DRAFT_LENGTH
      )
        throw new Error("invalid local receipt");
      return {
        clientInputId: item.clientInputId,
        text: item.text,
        directive:
          typeof item.directive === "string"
            ? fromJsonString(SessionDirectiveSchema, item.directive)
            : undefined,
        phase: "recovering",
      };
    });
    // A corrupt cursor must not discard otherwise valid, irreplaceable receipts.
    let cursor =
      typeof value.cursor === "string" && /^\d{1,19}$/.test(value.cursor)
        ? BigInt(value.cursor)
        : 0n;
    if (cursor > MAX_CURSOR) cursor = 0n;
    return { draft: value.draft, pending, cursor };
  } catch {
    return empty;
  }
}
function encode(value: SessionContinuity): string {
  if (
    value.draft.length > MAX_DRAFT_LENGTH ||
    value.pending.length > 16 ||
    value.pending.some(
      (item) =>
        !item.clientInputId ||
        item.clientInputId.length > 128 ||
        item.text.length > MAX_DRAFT_LENGTH,
    ) ||
    value.cursor < 0n ||
    value.cursor > MAX_CURSOR
  )
    throw new Error("journal limit");
  const record = JSON.stringify({
    draft: value.draft,
    cursor: value.cursor.toString(),
    pending: value.pending.map((item) => ({
      clientInputId: item.clientInputId,
      text: item.text,
      directive: item.directive ? toJsonString(SessionDirectiveSchema, item.directive) : undefined,
    })),
  });
  if (record.length > MAX_RECORD_BYTES) throw new Error("journal limit");
  return record;
}
export async function readSessionContinuity(key: string): Promise<SessionContinuity> {
  const empty = { draft: "", pending: [], cursor: 0n, epoch: "" };
  if (!key) return empty;
  const db = await open();
  if (!db) return empty;
  return new Promise((resolve) => {
    try {
      const tx = db.transaction(STORE, "readonly");
      const store = tx.objectStore(STORE);
      const record = store.get(key);
      const epoch = store.get(EPOCH_KEY);
      tx.oncomplete = () => {
        resolve({
          ...decode(record.result),
          epoch: typeof epoch.result === "string" ? epoch.result : "",
        });
      };
      tx.onabort = tx.onerror = () => {
        resolve(empty);
      };
    } catch {
      resolve(empty);
    }
  });
}
// Each patch reads and writes inside one IDB transaction. Idle/read-only tabs
// never write drafts or replace another tab's receipt set. Receipt insert must
// commit before the caller starts a potentially side-effectful RPC.
export async function patchSessionContinuity(
  key: string,
  patch: ContinuityPatch,
  guard?: ContinuityGuard,
): Promise<boolean> {
  if (!key || (guard && guard.generation !== clearGeneration)) return false;
  const db = await open();
  if (!db || (guard && guard.generation !== clearGeneration)) return false;
  return new Promise((resolve) => {
    try {
      const tx = db.transaction(STORE, "readwrite");
      const store = tx.objectStore(STORE);
      const record = store.get(key);
      const epoch = store.get(EPOCH_KEY);
      let success = false;
      epoch.onsuccess = () => {
        try {
          if (
            guard &&
            (guard.generation !== clearGeneration ||
              guard.epoch !== (typeof epoch.result === "string" ? epoch.result : ""))
          ) {
            tx.abort();
            return;
          }
          const current = decode(record.result);
          const receipts = new Map(current.pending.map((item) => [item.clientInputId, item]));
          for (const item of patch.addPending ?? []) receipts.set(item.clientInputId, item);
          for (const id of patch.removePendingIds ?? []) receipts.delete(id);
          current.pending = [...receipts.values()];
          if (patch.draft !== undefined) current.draft = patch.draft;
          if (patch.clearDraftIf !== undefined && current.draft === patch.clearDraftIf)
            current.draft = "";
          if (patch.resetCursor !== undefined) current.cursor = patch.resetCursor;
          else if (patch.cursor !== undefined && patch.cursor > current.cursor)
            current.cursor = patch.cursor;
          store.put(encode(current), key);
          success = true;
        } catch {
          tx.abort();
        }
      };
      tx.oncomplete = () => {
        resolve(success);
      };
      tx.onabort = tx.onerror = () => {
        resolve(false);
      };
    } catch {
      resolve(false);
    }
  });
}
// Compatibility helper for explicit writes. Pending entries merge; an omitted
// receipt never means deletion. Production uses the narrower patch interface.
export function writeSessionContinuity(key: string, value: SessionContinuity): Promise<boolean> {
  return patchSessionContinuity(key, {
    draft: value.draft,
    addPending: value.pending,
    cursor: value.cursor,
  });
}
export async function clearSessionContinuity(): Promise<void> {
  clearGeneration++;
  const db = await open();
  if (db)
    await new Promise<void>((resolve) => {
      try {
        const tx = db.transaction(STORE, "readwrite");
        const store = tx.objectStore(STORE);
        store.clear();
        store.put(crypto.randomUUID(), EPOCH_KEY);
        tx.oncomplete =
          tx.onabort =
          tx.onerror =
            () => {
              resolve();
            };
      } catch {
        resolve();
      }
    });
  // Also remove pre-IDB journal records; no content remains after Forget.
  try {
    for (const key of Object.keys(localStorage)) {
      if (key.startsWith(PREFIX)) localStorage.removeItem(key);
    }
  } catch {
    /* Browser storage may be disabled. */
  }
}

// Every caller waits for a snapshot begun after its request. Concurrent callers
// coalesce into the next batch; a slow snapshot still paints instead of being
// discarded forever by a faster polling interval.
export function coalesceSessionRefresh(load: () => Promise<boolean>): () => Promise<boolean> {
  let running = false;
  let waiters: ((value: boolean) => void)[] = [];
  const drain = async () => {
    running = true;
    while (waiters.length) {
      const batch = waiters;
      waiters = [];
      let result = false;
      try {
        result = await load();
      } catch {
        /* Caller reports errors. */
      }
      for (const done of batch) done(result);
    }
    running = false;
  };
  return () =>
    new Promise<boolean>((resolve) => {
      waiters.push(resolve);
      if (!running) void drain();
    });
}

export function sessionRetryDelay(milliseconds: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const finish = () => {
      window.clearTimeout(timer);
      signal.removeEventListener("abort", finish);
      resolve();
    };
    const timer = window.setTimeout(finish, milliseconds);
    signal.addEventListener("abort", finish, { once: true });
  });
}
