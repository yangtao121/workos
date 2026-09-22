import { fromJsonString, toJsonString } from "@bufbuild/protobuf";
import { SessionDirectiveSchema, type SessionDirective } from "@workos/protocol";

// Content belongs in this separate, device-local draft journal, never in the
// shared desktop/layout projection. Origin is supplied by browser storage;
// owner/project/session form the remaining isolation boundary.
const PREFIX = "workos.session-continuity.v1/";
const MAX_RECORD_BYTES = 512 * 1024;
export const MAX_DRAFT_LENGTH = 16_384;
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
}
export function sessionContinuityKey(owner: string, project: string, session: string): string {
  return owner && project && session
    ? PREFIX + [owner, project, session].map(encodeURIComponent).join("/")
    : "";
}
export function readSessionContinuity(key: string): SessionContinuity {
  const empty = { draft: "", pending: [], cursor: 0n };
  if (!key) return empty;
  try {
    const raw = localStorage.getItem(key);
    if (!raw || raw.length > MAX_RECORD_BYTES) return empty;
    const value = JSON.parse(raw) as {
      draft?: unknown;
      pending?: { clientInputId?: unknown; text?: unknown; directive?: unknown }[];
      cursor?: unknown;
    };
    if (
      typeof value.draft !== "string" ||
      value.draft.length > MAX_DRAFT_LENGTH ||
      !Array.isArray(value.pending) ||
      value.pending.length > 16 ||
      typeof value.cursor !== "string" ||
      !/^\d{1,19}$/.test(value.cursor)
    )
      return empty;
    const pending = value.pending.map((item): PendingSessionInput => {
      if (
        typeof item.clientInputId !== "string" ||
        item.clientInputId.length > 128 ||
        !item.clientInputId ||
        typeof item.text !== "string" ||
        item.text.length > MAX_DRAFT_LENGTH
      ) {
        throw new Error("invalid local receipt");
      }
      return {
        clientInputId: item.clientInputId,
        text: item.text,
        directive:
          typeof item.directive === "string"
            ? fromJsonString(SessionDirectiveSchema, item.directive)
            : undefined,
        // Restoring never submits; query the receipt first.
        phase: "recovering",
      };
    });
    return { draft: value.draft, pending, cursor: BigInt(value.cursor) };
  } catch {
    return empty;
  }
}
export function writeSessionContinuity(key: string, value: SessionContinuity): boolean {
  if (!key) return false;
  try {
    const record = JSON.stringify({
      draft: value.draft,
      cursor: value.cursor.toString(),
      pending: value.pending.map((item) => ({
        clientInputId: item.clientInputId,
        text: item.text,
        directive: item.directive
          ? toJsonString(SessionDirectiveSchema, item.directive)
          : undefined,
      })),
    });
    if (
      record.length > MAX_RECORD_BYTES ||
      value.pending.length > 16 ||
      value.draft.length > MAX_DRAFT_LENGTH
    )
      return false;
    localStorage.setItem(key, record);
    return true;
  } catch {
    return false;
  }
}
export function clearSessionContinuity(): void {
  try {
    for (const key of Object.keys(localStorage)) {
      if (key.startsWith(PREFIX)) localStorage.removeItem(key);
    }
  } catch {
    /* Storage can be disabled; authentication cleanup still proceeds. */
  }
}

// Waits abortably, including when a page is hidden/unmounted. No polling loop
// may survive its component or issue a late RPC under a different session.
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
