// @vitest-environment node
import type { Client } from "@connectrpc/connect";
import {
  NotificationChangeType,
  NotificationKind,
  NotificationOrigin,
  NotificationSeverity,
  NotificationTargetKind,
  type Notification as WireNotification,
  type NotificationEvent,
  type NotificationService,
} from "@workos/protocol";
import { afterEach, describe, expect, it } from "vitest";
import {
  createNotificationProjection,
  type NotificationProjectionSnapshot,
} from "./notifications.js";

const projections: ReturnType<typeof createNotificationProjection>[] = [];

afterEach(() => {
  for (const projection of projections.splice(0)) projection.stop();
});

function wireNotification(
  id: string,
  revision: bigint,
  createdSeconds: bigint,
  read = false,
): WireNotification {
  return {
    id,
    projectId: "01999999-9999-7999-8999-000000000001",
    kind: NotificationKind.AGENT_TASK_TERMINAL,
    severity: NotificationSeverity.NORMAL,
    origin: NotificationOrigin.SYSTEM,
    title: `Notification ${id}`,
    body: "bounded fixture",
    target: {
      kind: NotificationTargetKind.TASK,
      targetId: "01999999-9999-7999-8999-000000000002",
      appId: "",
      appInstallationId: "",
    },
    createdAt: { seconds: createdSeconds, nanos: 0 },
    readAt: read ? { seconds: createdSeconds + 1n, nanos: 0 } : undefined,
    revision,
  } as unknown as WireNotification;
}

function createdEvent(sequence: bigint, notification: WireNotification): NotificationEvent {
  return {
    changeSequence: sequence,
    notificationId: notification.id,
    type: NotificationChangeType.CREATED,
    revision: sequence,
    unreadCount: sequence,
    notification,
  } as NotificationEvent;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

async function waitForSnapshot(
  projection: ReturnType<typeof createNotificationProjection>,
  predicate: (snapshot: NotificationProjectionSnapshot) => boolean,
): Promise<NotificationProjectionSnapshot> {
  return await new Promise((resolve, reject) => {
    let unsubscribe: () => void = () => undefined;
    const timeout = setTimeout(() => {
      unsubscribe();
      reject(new Error("notification projection did not converge"));
    }, 2_000);
    unsubscribe = projection.subscribe((snapshot) => {
      if (!predicate(snapshot)) return;
      clearTimeout(timeout);
      unsubscribe();
      resolve(snapshot);
    });
  });
}

describe("notification projection", () => {
  it("keeps only the newest 100 streamed facts", async () => {
    async function* events() {
      for (let index = 1; index <= 105; index++) {
        const id = `01999999-9999-7999-8999-${String(index).padStart(12, "0")}`;
        yield {
          payload: {
            case: "event",
            value: createdEvent(BigInt(index), wireNotification(id, BigInt(index), BigInt(index))),
          },
        };
      }
      await new Promise(() => undefined);
    }
    const client = {
      getNotificationSummary: () => Promise.resolve({ unreadCount: 0n, incidentSourceReady: true }),
      listNotifications: () =>
        Promise.resolve({
          notifications: [],
          unreadCount: 0n,
          watermark: 0n,
          nextPageToken: "",
        }),
      watchNotificationEvents: () => events(),
    } as unknown as Client<typeof NotificationService>;
    const projection = createNotificationProjection(client);
    projections.push(projection);

    const latestID = "01999999-9999-7999-8999-000000000105";
    const snapshot = await waitForSnapshot(
      projection,
      (candidate) => candidate.notifications[0]?.id === latestID,
    );
    expect(snapshot.notifications).toHaveLength(100);
    expect(snapshot.notifications.at(-1)?.id).toBe("01999999-9999-7999-8999-000000000006");
  });

  it("does not clear live facts when an overlapping refresh is older", async () => {
    const initial = wireNotification("01999999-9999-7999-8999-000000000010", 5n, 10n, true);
    const live = wireNotification("01999999-9999-7999-8999-000000000011", 1n, 11n);
    const stalePage = deferred<{
      notifications: WireNotification[];
      unreadCount: bigint;
      watermark: bigint;
      nextPageToken: string;
    }>();
    let listCalls = 0;
    async function* events() {
      const staleCreated = createdEvent(6n, wireNotification(initial.id, 1n, 10n));
      staleCreated.revision = 1n;
      yield { payload: { case: "event", value: staleCreated } };
      yield { payload: { case: "event", value: createdEvent(7n, live) } };
      await new Promise(() => undefined);
    }
    const client = {
      getNotificationSummary: () => Promise.resolve({ unreadCount: 1n, incidentSourceReady: true }),
      listNotifications: async () => {
        listCalls++;
        if (listCalls <= 2) {
          return { notifications: [initial], unreadCount: 0n, watermark: 5n, nextPageToken: "" };
        }
        return await stalePage.promise;
      },
      watchNotificationEvents: () => events(),
    } as unknown as Client<typeof NotificationService>;
    const projection = createNotificationProjection(client);
    projections.push(projection);
    await projection.refresh();
    const liveSnapshot = await waitForSnapshot(projection, (candidate) =>
      candidate.notifications.some((notification) => notification.id === live.id),
    );
    expect(
      liveSnapshot.notifications.find((notification) => notification.id === initial.id)?.readAt,
    ).not.toBeNull();

    const refresh = projection.refresh();
    stalePage.resolve({
      notifications: [initial],
      unreadCount: 0n,
      watermark: 5n,
      nextPageToken: "",
    });
    await refresh;
    let finalSnapshot: NotificationProjectionSnapshot | undefined;
    const unsubscribe = projection.subscribe((snapshot) => {
      finalSnapshot = snapshot;
    });
    unsubscribe();
    expect(finalSnapshot?.notifications.map((notification) => notification.id)).toEqual([
      live.id,
      initial.id,
    ]);
    expect(
      finalSnapshot?.notifications.find((notification) => notification.id === initial.id)?.revision,
    ).toBe(5n);
  });
});
