// Resumable owner notification projection (ADR-0014). The projection is an
// in-memory, discardable cache of Core authority: it reconciles an
// authoritative snapshot with the owner-wide change stream, applies events
// idempotently by (change sequence, notification id, revision), reconnects
// with bounded backoff from the last applied cursor, and rebuilds from an
// authoritative full list whenever the server answers RESET_REQUIRED.
// Cursors and facts never enter URLs, DOM attributes, or localStorage.
import type { Client } from "@connectrpc/connect";
import {
  NotificationChangeType,
  type Notification as WireNotification,
  type NotificationService,
} from "@workos/protocol";

// Bounds shared with the Core transport.
const MAX_PAGE_SIZE = 100;
const RECONNECT_MIN_MS = 500;
const RECONNECT_MAX_MS = 10_000;

export type NotificationStreamState =
  | "idle"
  | "loading"
  | "live"
  | "reconnecting"
  | "resync"
  | "unavailable";

export interface NotificationView {
  id: string;
  projectId: string;
  kind: string;
  severity: string;
  origin: string;
  title: string;
  body: string;
  targetKind: string;
  targetId: string;
  appId: string;
  createdAt: Date;
  /** null while unread; read is monotonic. */
  readAt: Date | null;
  revision: bigint;
}

export interface NotificationProjectionSnapshot {
  state: NotificationStreamState;
  notifications: NotificationView[];
  unreadCount: number;
  incidentSourceReady: boolean;
}

export interface NotificationProjection {
  subscribe(listener: (snapshot: NotificationProjectionSnapshot) => void): () => void;
  refresh(): Promise<void>;
  markRead(notificationId: string): Promise<void>;
  markVisibleRead(notificationIds: string[]): Promise<void>;
  stop(): void;
}

type NotificationEventLike = {
  changeSequence: bigint;
  type: NotificationChangeType;
  notificationId: string;
  revision: bigint;
  notification?: WireNotification | undefined;
  unreadCount: bigint;
};

function notificationKindName(kind: number | string): string {
  // The generated enum keeps the proto names; project to the canonical
  // stored strings for rendering.
  const names: Record<number, string> = {
    0: "unspecified",
    1: "agent.approval.required",
    2: "agent.task.terminal",
    3: "artifact.review.created",
    4: "reliability.incident.opened",
    5: "app.instance.message",
  };
  if (typeof kind === "number") return names[kind] ?? "unspecified";
  return kind;
}

function targetKindName(kind: number): string {
  const names: Record<number, string> = {
    0: "unspecified",
    1: "approval",
    2: "task",
    3: "artifact",
    4: "incident",
    5: "app",
  };
  return names[kind] ?? "unspecified";
}

export function createNotificationProjection(
  notifications: Client<typeof NotificationService>,
): NotificationProjection {
  const byId = new Map<string, NotificationView>();
  let order: string[] = [];
  let cursor = 0n;
  let unreadCount = 0;
  let incidentSourceReady = false;
  let state: NotificationStreamState = "idle";
  let stopped = false;
  const listeners = new Set<(snapshot: NotificationProjectionSnapshot) => void>();

  function publish() {
    const snapshot: NotificationProjectionSnapshot = {
      state,
      notifications: order.map((id) => byId.get(id)!).filter(Boolean),
      unreadCount,
      incidentSourceReady,
    };
    for (const listener of listeners) listener(snapshot);
  }

  function applyView(
    notification: WireNotification,
    overrides?: { readAt?: Date | null; revision?: bigint }
  ) {
    byId.set(notification.id, {
      id: notification.id,
      projectId: notification.projectId,
      kind: notificationKindName(notification.kind),
      severity: notification.severity === 2 ? "critical" : "normal",
      origin: notification.origin === 2 ? "app" : "system",
      title: notification.title,
      body: notification.body,
      targetKind: notification.target ? targetKindName(notification.target.kind) : "unspecified",
      targetId: notification.target?.targetId ?? "",
      appId: notification.target?.appId ?? "",
      createdAt: timestampToDate(notification.createdAt) ?? new Date(0),
      readAt: overrides?.readAt ?? timestampToDate(notification.readAt),
      revision: overrides?.revision ?? notification.revision,
    });
  }

  function applyEvent(event: NotificationEventLike): boolean {
    // The cursor is the durable change sequence: only strictly newer
    // events apply; duplicates and stale revisions are inert.
    if (event.changeSequence <= cursor) return false;
    const existing = byId.get(event.notificationId);
    if (event.type === NotificationChangeType.READ) {
      if (existing && !existing.readAt) {
        existing.readAt = timestampToDate(event.notification?.readAt);
        existing.revision = event.revision;
      }
      unreadCount = Number(event.unreadCount);
      cursor = event.changeSequence;
      return true;
    }
    if (event.notification) {
      const wasRead = existing?.readAt != null;
      // A local read fact is monotonic: a duplicated CREATED replay can
      // never resurrect unread state.
      applyView(event.notification, {
        ...(wasRead ? { readAt: existing!.readAt } : {}),
        revision: event.revision,
      });
      order = [event.notificationId, ...order.filter((id) => id !== event.notificationId)];
      unreadCount = Number(event.unreadCount);
      cursor = event.changeSequence;
      return true;
    }
    cursor = event.changeSequence;
    return false;
  }

  async function resync() {
    state = "loading";
    publish();
    // Authoritative snapshot first, then the stream from its watermark, so
    // nothing is lost in the list/watch window.
    const summary = await notifications.getNotificationSummary({});
    unreadCount = Number(summary.unreadCount);
    incidentSourceReady = summary.incidentSourceReady;
    byId.clear();
    order = [];
    let pageToken = "";
    for (;;) {
      const page = await notifications.listNotifications({
        pageSize: MAX_PAGE_SIZE,
        pageToken,
      });
      for (const notification of page.notifications) applyView(notification);
      order = [...order, ...page.notifications.map((n: { id: string }) => n.id)];
      cursor = page.watermark;
      unreadCount = Number(page.unreadCount);
      if (!page.nextPageToken) break;
      pageToken = page.nextPageToken;
    }
    state = "live";
    publish();
  }

  async function watchLoop() {
    let backoff = RECONNECT_MIN_MS;
    while (!stopped) {
      try {
        for await (const response of notifications.watchNotificationEvents({
          afterSequence: cursor,
        })) {
          backoff = RECONNECT_MIN_MS;
          if (stopped) return;
          const payload = response.payload;
          if (payload.case === "heartbeat") continue;
          if (payload.case === "resetRequired") {
            state = "resync";
            publish();
            await resync();
            continue;
          }
          if (payload.case === "event") {
            applyEvent(payload.value);
            publish();
          }
        }
        // Bounded stream lifetime: the server ended the stream; reconnect
        // from the last applied cursor.
        if (stopped) return;
        state = "reconnecting";
        publish();
        await sleep(backoff);
        backoff = Math.min(backoff * 2, RECONNECT_MAX_MS);
      } catch (error) {
        if (stopped) return;
        state = "unavailable";
        publish();
        await sleep(backoff);
        backoff = Math.min(backoff * 2, RECONNECT_MAX_MS);
        void error;
        // Recover: rebuild from authority before reopening the stream.
        try {
          await resync();
          continue;
        } catch {
          continue;
        }
      }
    }
  }

  async function run() {
    for (;;) {
      if (stopped) return;
      try {
        await resync();
        break;
      } catch {
        state = "unavailable";
        publish();
        await sleep(RECONNECT_MAX_MS);
      }
    }
    void watchLoop();
  }

  let started = false;
  function ensureStarted() {
    if (!started) {
      started = true;
      void run();
    }
  }

  return {
    subscribe(listener) {
      ensureStarted();
      listeners.add(listener);
      listener({
        state,
        notifications: order.map((id) => byId.get(id)!).filter(Boolean),
        unreadCount,
        incidentSourceReady,
      });
      return () => listeners.delete(listener);
    },
    async refresh() {
      await resync();
    },
    async markRead(notificationId) {
      // The server is the read authority; the change stream converges the
      // badge on every paired device, including this one.
      await notifications.markNotificationRead({
        notificationId,
        idempotencyKey: crypto.randomUUID(),
      });
    },
    async markVisibleRead(notificationIds) {
      if (notificationIds.length === 0) return;
      await notifications.markNotificationsRead({
        notificationIds,
        idempotencyKey: crypto.randomUUID(),
      });
    },
    stop() {
      stopped = true;
      listeners.clear();
    },
  };
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// timestampToDate converts a protobuf Timestamp (seconds: bigint) without
// importing @bufbufbuf helper surface into the projection.
function timestampToDate(timestamp: { seconds: bigint } | undefined | null): Date | null {
  if (!timestamp) return null;
  return new Date(Number(timestamp.seconds) * 1000);
}
