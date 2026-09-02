import { useCallback, useMemo, useState } from "react";
import type {
  NotificationProjectionSnapshot,
  NotificationStreamState,
  NotificationView,
} from "@workos/agent-sdk";
import { Button } from "@workos/ui-kit";

// The Notification Center (ADR-0014) is a normal, non-permanent window over
// the owner's durable notification projection. Everything here is inert
// plain text; typed actions re-read their authoritative target through the
// public services before the Desktop opens the matching window, and a stale
// target shows the fixed verdict with an explicit mark-read — never an
// arbitrary fallback.
export interface NotificationCenterProps {
  snapshot: NotificationProjectionSnapshot;
  activeProjectId: string | undefined;
  onRefresh: () => Promise<void>;
  onMarkRead: (notificationId: string) => Promise<void>;
  onMarkVisibleRead: (notificationIds: string[]) => Promise<void>;
  onOpenTarget: (notification: NotificationView) => Promise<"opened" | "stale">;
}

type Filter = "all" | "project" | "unread";

const FILTERS: Array<[Filter, string]> = [
  ["all", "All"],
  ["project", "Current Project"],
  ["unread", "Unread"],
];

function actionLabel(targetKind: string): string {
  switch (targetKind) {
    case "approval":
      return "Open approval";
    case "task":
      return "Open task";
    case "artifact":
      return "Open artifact";
    case "incident":
      return "Open incident";
    case "app":
      return "Open app";
  }
  return "Open";
}

function streamCopy(state: NotificationStreamState): string {
  switch (state) {
    case "loading":
      return "Loading notifications…";
    case "reconnecting":
      return "Connection lost — catching up from your last update…";
    case "resync":
      return "Refreshing from the server…";
    case "unavailable":
      return "Notifications are temporarily unavailable.";
    default:
      return "";
  }
}

export function NotificationCenter({
  snapshot,
  activeProjectId,
  onRefresh,
  onMarkRead,
  onMarkVisibleRead,
  onOpenTarget,
}: NotificationCenterProps) {
  const [filter, setFilter] = useState<Filter>("all");
  const [busyIds, setBusyIds] = useState<Set<string>>(new Set());
  const [staleIds, setStaleIds] = useState<Set<string>>(new Set());
  const [actionError, setActionError] = useState<string>();

  const visible = useMemo(() => {
    if (filter === "project") {
      return snapshot.notifications.filter((n) => n.projectId === activeProjectId);
    }
    if (filter === "unread") {
      return snapshot.notifications.filter((n) => n.readAt == null);
    }
    return snapshot.notifications;
  }, [snapshot.notifications, filter, activeProjectId]);

  const unreadInScope = visible.filter((n) => n.readAt == null);

  const openTarget = useCallback(
    async (notification: NotificationView) => {
      setActionError(undefined);
      setBusyIds((current) => new Set(current).add(notification.id));
      try {
        const verdict = await onOpenTarget(notification);
        if (verdict === "stale") {
          setStaleIds((current) => new Set(current).add(notification.id));
        } else {
          setStaleIds((current) => {
            const next = new Set(current);
            next.delete(notification.id);
            return next;
          });
        }
      } catch {
        setActionError("The action could not be completed. Try again.");
      } finally {
        setBusyIds((current) => {
          const next = new Set(current);
          next.delete(notification.id);
          return next;
        });
      }
    },
    [onOpenTarget],
  );

  const markVisibleRead = useCallback(async () => {
    setActionError(undefined);
    try {
      await onMarkVisibleRead(unreadInScope.map((n) => n.id));
    } catch {
      setActionError("Mark read failed. Try again.");
    }
  }, [onMarkVisibleRead, unreadInScope]);

  const streamBanner = streamCopy(snapshot.state);
  return (
    <div className="notification-center" data-testid="notification-center">
      <div className="notification-toolbar" role="toolbar" aria-label="Notification filters">
        {FILTERS.map(([value, label]) => (
          <button
            aria-pressed={filter === value}
            className={filter === value ? "notification-filter active" : "notification-filter"}
            data-testid={`notification-filter-${value}`}
            key={value}
            onClick={() => {
              setFilter(value);
            }}
            type="button"
          >
            {label}
          </button>
        ))}
        <span className="notification-toolbar-spacer" />
        <Button
          disabled={unreadInScope.length === 0}
          onClick={() => {
            void markVisibleRead();
          }}
          type="button"
        >
          Mark visible read ({unreadInScope.length})
        </Button>
      </div>

      {streamBanner ? (
        <p
          className={
            snapshot.state === "unavailable"
              ? "notification-stream degraded"
              : "notification-stream"
          }
          data-testid="notification-stream-state"
          role="status"
        >
          {streamBanner}
          {snapshot.state === "unavailable" ? (
            <Button
              onClick={() => {
                void onRefresh();
              }}
              type="button"
            >
              Retry
            </Button>
          ) : null}
        </p>
      ) : null}
      {snapshot.state !== "unavailable" && !snapshot.incidentSourceReady ? (
        <p className="notification-stream degraded" role="note">
          Incident notifications may be delayed.
        </p>
      ) : null}
      {actionError ? (
        <p className="notification-stream degraded" role="alert">
          {actionError}
        </p>
      ) : null}

      {visible.length === 0 ? (
        <p className="empty-state" data-testid="notification-empty">
          {filter === "unread" ? "You are all caught up." : "No notifications yet."}
        </p>
      ) : (
        <ul className="notification-list">
          {visible.map((notification) => {
            const unread = notification.readAt == null;
            const stale = staleIds.has(notification.id);
            return (
              <li
                className={unread ? "notification-item unread" : "notification-item"}
                data-testid="notification-item"
                key={notification.id}
              >
                <span
                  aria-hidden="true"
                  className="notification-severity"
                  data-severity={notification.severity}
                />
                <div className="notification-body">
                  <p className="notification-title">
                    {notification.title}
                    {notification.origin === "app" ? (
                      <span className="notification-origin"> · app</span>
                    ) : null}
                  </p>
                  <p className="notification-text">{notification.body}</p>
                  {stale ? (
                    <p className="notification-stale" data-testid="notification-stale">
                      This item is no longer available.
                    </p>
                  ) : null}
                </div>
                <div className="notification-actions">
                  <Button
                    disabled={busyIds.has(notification.id)}
                    onClick={() => {
                      void openTarget(notification);
                    }}
                    type="button"
                  >
                    {actionLabel(notification.targetKind)}
                  </Button>
                  {unread ? (
                    <Button
                      disabled={busyIds.has(notification.id)}
                      className="workos-button secondary"
                      onClick={() => {
                        onMarkRead(notification.id).catch(() => {
                          setActionError("Mark read failed. Try again.");
                        });
                      }}
                      type="button"
                    >
                      Mark read
                    </Button>
                  ) : null}
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
