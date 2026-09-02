// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NotificationCenter } from "./NotificationCenter.js";
import type { NotificationProjectionSnapshot, NotificationView } from "@workos/agent-sdk";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const UNREAD_ID = "01990000-0000-7000-8000-00000000a001";
const READ_ID = "01990000-0000-7000-8000-00000000a002";
const OTHER_PROJECT_ID = "01990000-0000-7000-8000-00000000a003";

function view(overrides: Partial<NotificationView>): NotificationView {
  return {
    id: UNREAD_ID,
    projectId: OTHER_PROJECT_ID,
    kind: "agent.approval.required",
    severity: "normal",
    origin: "system",
    title: "Approval required",
    body: "A project agent task is waiting for your approval.",
    targetKind: "approval",
    targetId: "01990000-0000-7000-8000-00000000b001",
    appId: "",
    createdAt: new Date("2026-09-02T10:00:00Z"),
    readAt: null,
    revision: 4n,
    ...overrides,
  };
}

function snapshot(
  overrides: Partial<NotificationProjectionSnapshot>,
): NotificationProjectionSnapshot {
  return {
    state: "live",
    notifications: [
      view({}),
      view({
        id: READ_ID,
        kind: "artifact.review.created",
        targetKind: "artifact",
        title: "Review artifact ready",
        readAt: new Date("2026-09-02T11:00:00Z"),
      }),
    ],
    unreadCount: 1,
    incidentSourceReady: true,
    ...overrides,
  };
}

describe("NotificationCenter", () => {
  beforeEach(() => {
    render(
      <NotificationCenter
        snapshot={snapshot({})}
        activeProjectId={OTHER_PROJECT_ID}
        onRefresh={() => Promise.resolve()}
        onMarkRead={() => Promise.resolve()}
        onMarkVisibleRead={() => Promise.resolve()}
        onOpenTarget={() => Promise.resolve("opened")}
      />,
    );
  });
  afterEach(cleanup);

  it("lists notifications newest first with bounded filters", async () => {
    const items = screen.getAllByTestId("notification-item");
    expect(items).toHaveLength(2);
    expect(screen.getByText("Approval required")).toBeTruthy();
    await userEvent.click(screen.getByTestId("notification-filter-unread"));
    expect(screen.getAllByTestId("notification-item")).toHaveLength(1);
    await userEvent.click(screen.getByTestId("notification-filter-project"));
    expect(screen.getAllByTestId("notification-item")).toHaveLength(2);
  });

  it("shows the fixed empty states per filter", async () => {
    cleanup();
    render(
      <NotificationCenter
        snapshot={snapshot({ notifications: [] })}
        activeProjectId={undefined}
        onRefresh={() => Promise.resolve()}
        onMarkRead={() => Promise.resolve()}
        onMarkVisibleRead={() => Promise.resolve()}
        onOpenTarget={() => Promise.resolve("opened")}
      />,
    );
    expect(screen.getByTestId("notification-empty").textContent).toBe("No notifications yet.");
    await userEvent.click(screen.getByTestId("notification-filter-unread"));
    expect(screen.getByTestId("notification-empty").textContent).toBe("You are all caught up.");
  });

  it("surfaces bounded degraded and reconnecting stream states", () => {
    cleanup();
    render(
      <NotificationCenter
        snapshot={snapshot({ state: "reconnecting" })}
        activeProjectId={undefined}
        onRefresh={() => Promise.resolve()}
        onMarkRead={() => Promise.resolve()}
        onMarkVisibleRead={() => Promise.resolve()}
        onOpenTarget={() => Promise.resolve("opened")}
      />,
    );
    const banner = screen.getByTestId("notification-stream-state");
    expect(banner.textContent).toContain("catching up");
    cleanup();
    render(
      <NotificationCenter
        snapshot={snapshot({ state: "unavailable", incidentSourceReady: false })}
        activeProjectId={undefined}
        onRefresh={() => Promise.resolve()}
        onMarkRead={() => Promise.resolve()}
        onMarkVisibleRead={() => Promise.resolve()}
        onOpenTarget={() => Promise.resolve("opened")}
      />,
    );
    expect(screen.getByTestId("notification-stream-state").textContent).toContain(
      "temporarily unavailable",
    );
    expect(screen.getByText("Retry")).toBeTruthy();
    // The incident-freshness note is only shown while the stream itself is
    // healthy; a fully unavailable stream already explains the delay.
    cleanup();
    render(
      <NotificationCenter
        snapshot={snapshot({ state: "live", incidentSourceReady: false })}
        activeProjectId={undefined}
        onRefresh={() => Promise.resolve()}
        onMarkRead={() => Promise.resolve()}
        onMarkVisibleRead={() => Promise.resolve()}
        onOpenTarget={() => Promise.resolve("opened")}
      />,
    );
    expect(screen.getByText("Incident notifications may be delayed.")).toBeTruthy();
  });
});

describe("NotificationCenter actions", () => {
  afterEach(cleanup);

  it("re-reads the typed target before opening and shows the fixed stale verdict", async () => {
    const openedTargets: string[] = [];
    const staleRef: { value: boolean } = { value: true };
    const { rerender } = render(
      <NotificationCenter
        snapshot={snapshot({})}
        activeProjectId={undefined}
        onRefresh={() => Promise.resolve()}
        onMarkRead={() => Promise.resolve()}
        onMarkVisibleRead={() => Promise.resolve()}
        onOpenTarget={(notification) => {
          openedTargets.push(notification.targetId);
          return Promise.resolve(staleRef.value ? "stale" : "opened");
        }}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Open approval" }));
    expect(openedTargets).toEqual(["01990000-0000-7000-8000-00000000b001"]);
    expect(screen.getByTestId("notification-stale").textContent).toBe(
      "This item is no longer available.",
    );
    // Mark read stays available on a stale item.
    expect(screen.getByRole("button", { name: "Mark read" })).toBeTruthy();
    staleRef.value = false;
    rerender(
      <NotificationCenter
        snapshot={snapshot({})}
        activeProjectId={undefined}
        onRefresh={() => Promise.resolve()}
        onMarkRead={() => Promise.resolve()}
        onMarkVisibleRead={() => Promise.resolve()}
        onOpenTarget={(notification) => {
          openedTargets.push(notification.targetId);
          return Promise.resolve(staleRef.value ? "stale" : "opened");
        }}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Open approval" }));
    expect(screen.queryByTestId("notification-stale")).toBeNull();
  });

  it("marks the visible unread page read in one explicit command", async () => {
    const batches: string[][] = [];
    cleanup();
    render(
      <NotificationCenter
        snapshot={snapshot({})}
        activeProjectId={undefined}
        onRefresh={() => Promise.resolve()}
        onMarkRead={() => Promise.resolve()}
        onMarkVisibleRead={(ids) => {
          batches.push(ids);
          return Promise.resolve();
        }}
        onOpenTarget={() => Promise.resolve("opened")}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Mark visible read (1)" }));
    expect(batches).toHaveLength(1);
    expect(batches[0]).toEqual([UNREAD_ID]);
  });
});
