import { useCallback, useEffect, useRef, useState } from "react";
import type { Client } from "@connectrpc/connect";
import {
  NotificationOrigin,
  NotificationSeverity,
  NotificationTargetKind,
  type AppBridgeService,
} from "@workos/protocol";
import { BridgeProtocolError, encodeFileData, decodeFileData } from "@workos/surface-sdk";
import {
  openAppBridgeHost,
  type AppBridgeHost,
  type AppBridgeTransport,
  type AppBridgeShellHost,
} from "@workos/app-host";
import { useWorkspaceFilePicker } from "./WorkspaceFilePicker.js";
import { asBridgeProtocolError } from "./bridgeErrors.js";
import type { AppSurfaceRef } from "@workos/window-manager";

export type SurfaceWindowState = "loading" | "ready" | "closed";
export type BridgeState = "pending" | "ready" | "failed" | "unavailable";

/**
 * The surface's bridge credentials: the ephemeral token and the effective
 * capability list from CreateSurface. They live in the Desktop's plain ref —
 * never in serializable window state — and are handed to this component as
 * props only so the trusted host can present the token as RPC metadata. The
 * iframe never sees any of it.
 */
export interface SurfaceBridgeCredentials {
  token: string;
  capabilities: string[];
}

import {
  DeclarativeSurface,
  parseDeclarativeDoc,
  type DeclarativeDoc,
} from "./DeclarativeSurface.js";

export type AppSurfaceShellHost = AppBridgeShellHost;

interface AppSurfaceProps {
  surface: AppSurfaceRef;
  bridge?: SurfaceBridgeCredentials | undefined;
  appBridge: Client<typeof AppBridgeService>;
  /** Shell-side bridge host; when absent the shell methods fail closed. */
  shell?: AppSurfaceShellHost | undefined;
}

// notificationKindName projects the wire enum onto the canonical stored
// kind strings for the iframe projection.
function notificationKindName(kind: number): string {
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

// buildTransport projects the canonical AppBridgeService calls into the
// transport the trusted host uses. The token travels only as dedicated RPC
// metadata on the public bridge routes; the host itself never holds it.
function buildTransport(
  appBridge: Client<typeof AppBridgeService>,
  credentials: SurfaceBridgeCredentials,
): AppBridgeTransport {
  const headers = { "X-WorkOS-Bridge-Token": credentials.token };
  return {
    async listFiles(directory, after, signal) {
      try {
        const page = await appBridge.listFiles({ directory, after }, { headers, signal });
        return {
          entries: page.entries.map((entry) => {
            if (!entry.ref) throw new BridgeProtocolError("internal");
            const { projectId, path, etag } = entry.ref;
            return {
              ref: { projectId, path, etag },
              directory: entry.directory,
              sizeBytes: Number(entry.sizeBytes),
            };
          }),
          nextAfter: page.nextAfter,
        };
      } catch (error) {
        throw asBridgeProtocolError(error);
      }
    },
    async createArtifact(input, signal) {
      try {
        const { artifact } = await appBridge.createReviewArtifact(
          {
            idempotencyKey: input.idempotencyKey,
            type: input.type,
            title: input.title,
            content: decodeFileData(input.contentBase64),
          },
          { headers, signal },
        );
        if (!artifact) throw new BridgeProtocolError("internal");
        return {
          id: artifact.id,
          projectId: artifact.projectId,
          type: artifact.type,
          title: artifact.title,
          digest: artifact.digest,
        };
      } catch (error) {
        throw asBridgeProtocolError(error);
      }
    },
    async openArtifact(input, signal) {
      try {
        const { artifact } = await appBridge.openReviewArtifact(input, { headers, signal });
        if (!artifact) throw new BridgeProtocolError("internal");
        return {
          id: artifact.id,
          projectId: artifact.projectId,
          type: artifact.type,
          title: artifact.title,
          digest: artifact.digest,
        };
      } catch (error) {
        throw asBridgeProtocolError(error);
      }
    },
    async readFile(input, signal) {
      try {
        const result = await appBridge.readFile(input, { headers, signal });
        return { dataBase64: encodeFileData(result.data) };
      } catch (error) {
        throw asBridgeProtocolError(error);
      }
    },
    async writeFile(input, signal) {
      try {
        const result = await appBridge.writeFile(
          { ref: input.ref, data: decodeFileData(input.dataBase64) },
          { headers, signal },
        );
        if (!result.ref) throw new BridgeProtocolError("internal");
        const { projectId, path, etag } = result.ref;
        return { ref: { projectId, path, etag } };
      } catch (error) {
        throw asBridgeProtocolError(error);
      }
    },
    async authorizeShellAction(method) {
      try {
        await appBridge.authorizeShellAction({ method }, { headers });
      } catch (error) {
        throw asBridgeProtocolError(error);
      }
    },
    async runAgentTask(input) {
      try {
        const response = await appBridge.runAgentTask(
          {
            idempotencyKey: input.idempotencyKey,
            role: input.role ?? "",
            goal: input.goal,
          },
          { headers },
        );
        return {
          taskId: response.taskId,
          state: response.state.toString(),
          lastEventSequence: response.lastEventSequence.toString(),
        };
      } catch (reason: unknown) {
        // Stable, safe code projection: run and stream share one mapping.
        throw asBridgeProtocolError(reason);
      }
    },
    async searchKnowledge(input) {
      try {
        const response = await appBridge.searchKnowledge(
          {
            query: input.query,
            pageSize: input.pageSize ?? 0,
            pageToken: input.pageToken ?? "",
          },
          { headers },
        );
        return {
          hits: response.hits.map((hit) => ({
            artifactId: hit.artifactId,
            digest: hit.digest,
            artifactType: hit.artifactType,
            title: hit.title,
            excerpt: hit.excerpt,
            score: hit.score,
          })),
          nextPageToken: response.nextPageToken,
        };
      } catch (reason: unknown) {
        throw asBridgeProtocolError(reason);
      }
    },
    async createNotification(input) {
      try {
        const response = await appBridge.createNotification(
          {
            idempotencyKey: input.idempotencyKey,
            title: input.title,
            body: input.body ?? "",
          },
          { headers },
        );
        const notification = response.notification;
        if (!notification || !notification.target) {
          throw new BridgeProtocolError("internal");
        }
        return {
          notification: {
            id: notification.id,
            projectId: notification.projectId,
            kind: notificationKindName(notification.kind),
            severity:
              notification.severity === NotificationSeverity.CRITICAL ? "critical" : "normal",
            origin: notification.origin === NotificationOrigin.APP ? "app" : "system",
            title: notification.title,
            body: notification.body,
            targetKind:
              notification.target.kind === NotificationTargetKind.APP ? "app" : "unspecified",
            targetId: notification.target.targetId,
            appId: notification.target.appId,
          },
          unreadCount: Number(response.unreadCount),
        };
      } catch (reason: unknown) {
        throw asBridgeProtocolError(reason);
      }
    },
    watchAgentTaskEvents(input, onEvent, signal) {
      return new Promise((resolve, reject) => {
        void (async () => {
          try {
            let afterSequence: bigint;
            try {
              afterSequence = BigInt(input.afterSequence || "0");
            } catch {
              // A malformed local cursor is the caller's argument error, not
              // an availability problem.
              throw new BridgeProtocolError("invalid_argument");
            }
            for await (const response of appBridge.watchAgentTaskEvents(
              { taskId: input.taskId, afterSequence },
              { headers, signal },
            )) {
              if (response.event) {
                onEvent(response.event);
              }
            }
            resolve();
          } catch (reason: unknown) {
            reject(asBridgeProtocolError(reason));
          }
        })();
      });
    },
  };
}

// AppSurface renders one installed app inside a sandboxed iframe and hosts
// its App Bridge. The iframe gets scripts only — no same-origin, no forms,
// no popups, no storage — and never receives WorkOS clients or credentials.
// Every iframe load starts a fresh handshake: the old port is closed, old
// pending requests fail, and a new nonce/channel pair is minted for the
// exact new contentWindow.
export function AppSurface({ surface, bridge, appBridge, shell }: AppSurfaceProps) {
  const [state, setState] = useState<SurfaceWindowState>("loading");
  const [bridgeState, setBridgeState] = useState<BridgeState>("pending");
  const frameRef = useRef<HTMLIFrameElement>(null);
  const filePicker = useWorkspaceFilePicker();
  const hostRef = useRef<AppBridgeHost | undefined>(undefined);
  // Declarative probe (ADR-0016-era slice): a bundle may ship a versioned
  // `surface.json` declarative document instead of an interactive page. If
  // the probe finds one, WorkOS renders it with inert native components —
  // no iframe, no bridge. Otherwise the standard sandboxed iframe flow runs.
  const [declarative, setDeclarative] = useState<
    { mode: "probing" } | { mode: "native"; doc: DeclarativeDoc } | { mode: "iframe" }
  >({ mode: "probing" });

  useEffect(() => {
    let alive = true;
    const probe = async () => {
      try {
        const response = await fetch(`${surface.url}surface.json`, {
          credentials: "include",
          redirect: "error",
        });
        if (!response.ok) throw new Error("no declarative document");
        const raw = await response.text();
        const doc = parseDeclarativeDoc(raw);
        if (alive) setDeclarative({ mode: "native", doc });
      } catch {
        if (alive) setDeclarative({ mode: "iframe" });
      }
    };
    void probe();
    return () => {
      alive = false;
    };
  }, [surface.url]);

  const startHandshake = useCallback(() => {
    hostRef.current?.close();
    hostRef.current = undefined;
    if (!bridge || bridge.token === "") {
      setBridgeState("unavailable");
      return;
    }
    const frameWindow = frameRef.current?.contentWindow;
    if (!frameWindow) return;
    const transport = buildTransport(appBridge, bridge);
    const host = openAppBridgeHost({
      frameWindow,
      capabilities: bridge.capabilities,
      transport,
      filePicker: (input, signal) => filePicker.open(transport, input, signal),
      ...(shell === undefined ? {} : { shell }),
      onHandshakeComplete: () => {
        setBridgeState("ready");
      },
      onHandshakeFailed: () => {
        setBridgeState("failed");
      },
    });
    hostRef.current = host;
  }, [appBridge, bridge, filePicker.open]);

  useEffect(() => {
    setState("loading");
    setBridgeState("pending");
    return () => {
      // Reload, project switch, close, unmount: the old port dies and its
      // pending requests fail; the durable agent tasks keep running.
      hostRef.current?.close();
      hostRef.current = undefined;
    };
  }, [surface.url]);

  if (declarative.mode === "native") {
    return (
      <div className="app-surface-body">
        <DeclarativeSurface
          surfaceUrl={surface.url.endsWith("/") ? surface.url : `${surface.url}/`}
        />
      </div>
    );
  }

  return (
    <div className="app-surface-body">
      {filePicker.dialog}
      {state === "loading" ? (
        <p className="surface-state" role="status">
          Opening app surface…
        </p>
      ) : null}
      {state === "ready" && bridgeState === "pending" ? (
        <p className="surface-state" role="status">
          Connecting app bridge…
        </p>
      ) : null}
      {state === "ready" && bridgeState === "failed" ? (
        <div className="surface-state surface-bridge-error" role="alert">
          <p>The app bridge could not be established.</p>
          <button className="surface-retry" onClick={startHandshake} type="button">
            Retry bridge
          </button>
        </div>
      ) : null}
      {state === "ready" && bridgeState === "unavailable" ? (
        <div className="surface-state surface-bridge-error" role="alert">
          <p>This surface was opened without a bridge credential.</p>
        </div>
      ) : null}
      <iframe
        ref={frameRef}
        className="app-surface-frame"
        data-testid="app-surface-frame"
        onLoad={() => {
          setState((current) => (current === "closed" ? current : "ready"));
          startHandshake();
        }}
        referrerPolicy="no-referrer"
        sandbox="allow-scripts"
        src={surface.url}
        title={`App surface ${surface.surfaceSessionId}`}
      />
    </div>
  );
}
