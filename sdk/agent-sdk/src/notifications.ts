// Resumable owner notification projection (ADR-0014). The projection is an
// in-memory, discardable cache of Core authority: it reconciles an
// authoritative snapshot with the owner-wide change stream, applies events
// idempotently by (change sequence, notification id, revision), reconnects
// with bounded backoff from the last applied cursor, and rebuilds from an
// authoritative bounded latest page whenever the server answers RESET_REQUIRED.
// Cursors and facts never enter URLs, DOM attributes, or localStorage.
import type { Client } from "@connectrpc/connect";
import {
  NotificationChangeType,
  NotificationOrigin,
  NotificationSeverity,
  type Notification as WireNotification,
  type NotificationEvent,
  type NotificationService,
} from "@workos/protocol";

// Bounds shared with the Core transport.
const MAX_PAGE_SIZE = 100;
const MAX_PROJECTION_NOTIFICATIONS = 100;
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

function notificationKindName(kind: number): string {
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
  return names[kind] ?? "unspecified";
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

function viewsInOrder(
  order: readonly string[],
  byId: ReadonlyMap<string, NotificationView>,
): NotificationView[] {
  const views: NotificationView[] = [];
  for (const id of order) {
    const view = byId.get(id);
    if (view) views.push(view);
  }
  return views;
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
  // Read through a function: the loops below call it each iteration, so the
  // type checker never narrows a live flag that stop() flips from another
  // call stack.
  const isStopped = (): boolean => stopped;
  const listeners = new Set<(snapshot: NotificationProjectionSnapshot) => void>();

  function publish() {
    const snapshot: NotificationProjectionSnapshot = {
      state,
      notifications: viewsInOrder(order, byId),
      unreadCount,
      incidentSourceReady,
    };
    for (const listener of listeners) listener(snapshot);
  }

  function trimProjection() {
    if (order.length <= MAX_PROJECTION_NOTIFICATIONS) return;
    const removed = order.slice(MAX_PROJECTION_NOTIFICATIONS);
    order = order.slice(0, MAX_PROJECTION_NOTIFICATIONS);
    for (const id of removed) byId.delete(id);
  }

  function sortProjection() {
    order = [...byId.keys()].sort((leftID, rightID) => {
      const left = byId.get(leftID);
      const right = byId.get(rightID);
      if (!left || !right) return left ? -1 : right ? 1 : 0;
      const byTime = right.createdAt.getTime() - left.createdAt.getTime();
      return byTime !== 0 ? byTime : rightID.localeCompare(leftID);
    });
  }

  function maxRevision(...values: bigint[]): bigint {
    return values.reduce((highest, value) => (value > highest ? value : highest), 0n);
  }

  function applyView(
    notification: WireNotification,
    overrides?: { readAt?: Date | null; revision?: bigint },
  ) {
    byId.set(notification.id, {
      id: notification.id,
      projectId: notification.projectId,
      kind: notificationKindName(notification.kind),
      severity: notification.severity === NotificationSeverity.CRITICAL ? "critical" : "normal",
      origin: notification.origin === NotificationOrigin.APP ? "app" : "system",
      title: notification.title,
      body: notification.body,
      targetKind: notification.target ? targetKindName(notification.target.kind) : "unspecified",
      targetId: notification.target?.targetId ?? "",
      appId: notification.target?.appId ?? "",
      createdAt: timestampToDate(notification.createdAt) ?? new Date(0),
      readAt:
        overrides && "readAt" in overrides
          ? overrides.readAt
          : timestampToDate(notification.readAt),
      revision: overrides?.revision ?? notification.revision,
    });
  }

  function applyEvent(event: NotificationEvent): boolean {
    // The cursor is the durable change sequence: only strictly newer
    // events apply; duplicates and stale revisions are inert.
    if (event.changeSequence <= cursor) return false;
    const existing = byId.get(event.notificationId);
    if (event.type === NotificationChangeType.READ) {
      if (existing) {
        if (existing.readAt === null) {
          existing.readAt = timestampToDate(event.notification?.readAt);
        }
        existing.revision = maxRevision(
          existing.revision,
          event.revision,
          event.notification?.revision ?? 0n,
        );
      }
      unreadCount = Number(event.unreadCount);
      cursor = event.changeSequence;
      return true;
    }
    if (event.notification) {
      // A local read fact is monotonic: a duplicated CREATED replay can
      // never resurrect unread state.
      const readAt = existing?.readAt ?? timestampToDate(event.notification.readAt);
      applyView(event.notification, {
        readAt,
        revision: maxRevision(
          existing?.revision ?? 0n,
          event.revision,
          event.notification.revision,
        ),
      });
      sortProjection();
      trimProjection();
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
    // The desktop projection is intentionally bounded to the latest page;
    // older history remains pageable at the Core authority and never grows
    // a long-running browser process without limit.
    const page = await notifications.listNotifications({
      pageSize: MAX_PAGE_SIZE,
      pageToken: "",
    });
    if (page.watermark < cursor) {
      // A manual refresh may overlap the live stream. Never let a slower,
      // older snapshot roll the cursor or projection back after a newer event.
      state = "live";
      publish();
      return;
    }
    // Only replace the live projection after the candidate snapshot has
    // passed the cursor check. Clearing earlier would let an older overlapping
    // refresh keep the new cursor while publishing an empty fact set.
    byId.clear();
    order = [];
    for (const notification of page.notifications) applyView(notification);
    order = page.notifications.map((n) => n.id);
    cursor = page.watermark;
    unreadCount = Number(page.unreadCount);
    incidentSourceReady = summary.incidentSourceReady;
    state = "live";
    publish();
  }

  async function watchLoop() {
    let backoff = RECONNECT_MIN_MS;
    while (!isStopped()) {
      try {
        for await (const response of notifications.watchNotificationEvents({
          afterSequence: cursor,
        })) {
          backoff = RECONNECT_MIN_MS;
          if (isStopped()) return;
          const payload = response.payload;
          // Control frames never advance the durable cursor.
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
        if (isStopped()) return;
        state = "reconnecting";
        publish();
        await sleep(backoff);
        backoff = Math.min(backoff * 2, RECONNECT_MAX_MS);
      } catch {
        if (isStopped()) return;
        state = "unavailable";
        publish();
        await sleep(backoff);
        backoff = Math.min(backoff * 2, RECONNECT_MAX_MS);
        // Recover: rebuild from authority before reopening the stream.
        try {
          await resync();
        } catch {
          // stay unavailable and retry on the next loop
        }
      }
    }
  }

  async function run() {
    while (!isStopped()) {
      try {
        await resync();
        await watchLoop();
        return;
      } catch {
        if (isStopped()) return;
        state = "unavailable";
        publish();
        await sleep(RECONNECT_MAX_MS);
      }
    }
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
        notifications: viewsInOrder(order, byId),
        unreadCount,
        incidentSourceReady,
      });
      return () => {
        listeners.delete(listener);
      };
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
        notificationIds: notificationIds.slice(0, MAX_PROJECTION_NOTIFICATIONS),
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
// importing the protobuf helper surface into the projection.
function timestampToDate(timestamp: { seconds: bigint } | undefined | null): Date | null {
  if (!timestamp) return null;
  return new Date(Number(timestamp.seconds) * 1000);
}
