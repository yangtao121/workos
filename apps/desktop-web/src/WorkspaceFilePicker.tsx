import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { AppBridgeTransport } from "@workos/app-host";
import {
  BridgeProtocolError,
  type BridgeFileEntry,
  type BridgeFilePickPayload,
  type BridgeFileRef,
} from "@workos/surface-sdk";
import { Button, Icon } from "@workos/ui-kit";

interface PickerRequest {
  transport: AppBridgeTransport;
  multiple: boolean;
  finish: (refs: BridgeFileRef[]) => void;
}

export function useWorkspaceFilePicker() {
  const [request, setRequest] = useState<PickerRequest>();
  const active = useRef<PickerRequest | undefined>(undefined);
  const open = useCallback(
    (
      transport: AppBridgeTransport,
      input: BridgeFilePickPayload,
      signal: AbortSignal,
    ): Promise<BridgeFileRef[]> => {
      if (signal.aborted) return Promise.reject(new BridgeProtocolError("bridge_closed"));
      if (active.current) return Promise.reject(new BridgeProtocolError("too_many_inflight"));
      return new Promise((resolve, reject) => {
        const clear = () => {
          signal.removeEventListener("abort", abort);
          if (active.current === next) {
            active.current = undefined;
            setRequest(undefined);
          }
        };
        const abort = () => {
          clear();
          reject(new BridgeProtocolError("bridge_closed"));
        };
        const next: PickerRequest = {
          transport,
          multiple: input.multiple ?? false,
          finish: (refs) => {
            clear();
            resolve(refs);
          },
        };
        active.current = next;
        setRequest(next);
        signal.addEventListener("abort", abort, { once: true });
      });
    },
    [],
  );
  return {
    open,
    dialog: request ? createPortal(<WorkspaceFilePicker request={request} />, document.body) : null,
  };
}
function WorkspaceFilePicker({ request }: { request: PickerRequest }) {
  const [directory, setDirectory] = useState("");
  const [after, setAfter] = useState("");
  const [nextAfter, setNextAfter] = useState("");
  const [entries, setEntries] = useState<BridgeFileEntry[]>([]);
  const [selected, setSelected] = useState<Map<string, BridgeFileRef>>(new Map());
  const [state, setState] = useState<"loading" | "ready" | "error">("loading");
  const [attempt, setAttempt] = useState(0);
  const dialog = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const previous = document.activeElement;
    dialog.current?.querySelector<HTMLButtonElement>("button")?.focus();
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
    };
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    setState("loading");
    void request.transport
      .listFiles(directory, after, controller.signal)
      .then((page) => {
        if (controller.signal.aborted) return;
        setEntries((current) => (after ? [...current, ...page.entries] : page.entries));
        setNextAfter(page.nextAfter);
        setState("ready");
      })
      .catch(() => {
        if (!controller.signal.aborted) setState("error");
      });
    return () => {
      controller.abort();
    };
  }, [request, directory, after, attempt]);
  function navigate(path: string) {
    setDirectory(path);
    setAfter("");
    setEntries([]);
  }
  return (
    <div
      className="workspace-picker-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) request.finish([]);
      }}
    >
      <div
        className="workspace-picker"
        role="dialog"
        aria-modal="true"
        aria-label="Choose workspace files"
        ref={dialog}
        onKeyDown={(event) => {
          event.stopPropagation();
          if (event.key === "Escape") {
            event.preventDefault();
            request.finish([]);
          }
          if (event.key === "Tab") {
            const focusable = dialog.current?.querySelectorAll<HTMLElement>(
              "button:not(:disabled), input:not(:disabled)",
            );
            const first = focusable?.[0],
              last = focusable?.[focusable.length - 1];
            if (event.shiftKey && document.activeElement === first) {
              event.preventDefault();
              last?.focus();
            } else if (!event.shiftKey && document.activeElement === last) {
              event.preventDefault();
              first?.focus();
            }
          }
        }}
      >
        <header>
          <div>
            <h2>Choose files</h2>
            <p>Files up to 32 KiB are available to this app.</p>
          </div>
          <Button
            onClick={() => {
              request.finish([]);
            }}
          >
            Cancel
          </Button>
        </header>
        <div className="workspace-picker-path">
          <Button
            disabled={!directory}
            onClick={() => {
              navigate(directory.split("/").slice(0, -1).join("/"));
            }}
          >
            Back
          </Button>
          <span>Workspace{directory ? ` / ${directory}` : ""}</span>
        </div>
        <div className="workspace-picker-list">
          {entries.map((entry) =>
            entry.directory ? (
              <button
                type="button"
                key={entry.ref.path}
                className="workspace-picker-folder"
                onClick={() => {
                  navigate(entry.ref.path);
                }}
              >
                <Icon name="files" size={18} />
                {entry.ref.path.split("/").at(-1)}
              </button>
            ) : (
              <label key={entry.ref.path} className="workspace-picker-file">
                <input
                  type="checkbox"
                  checked={selected.has(entry.ref.path)}
                  disabled={
                    request.multiple && selected.size >= 20 && !selected.has(entry.ref.path)
                  }
                  onChange={(event) => {
                    const next = request.multiple
                      ? new Map(selected)
                      : new Map<string, BridgeFileRef>();
                    if (event.target.checked) next.set(entry.ref.path, entry.ref);
                    else next.delete(entry.ref.path);
                    setSelected(next);
                  }}
                />
                <Icon name="docs" size={18} />
                <span>{entry.ref.path.split("/").at(-1)}</span>
                <small>{entry.sizeBytes} B</small>
              </label>
            ),
          )}
          {state === "loading" ? (
            <p role="status">Loading files…</p>
          ) : state === "error" ? (
            <div role="alert">
              <p>Files could not be loaded.</p>
              <Button
                onClick={() => {
                  setAttempt((value) => value + 1);
                }}
              >
                Retry
              </Button>
            </div>
          ) : entries.length === 0 ? (
            <p>No available files in this folder.</p>
          ) : null}
          {nextAfter && state === "ready" ? (
            <Button
              onClick={() => {
                setAfter(nextAfter);
              }}
            >
              Load more
            </Button>
          ) : null}
        </div>
        <footer>
          <span>
            {selected.size} selected{request.multiple ? " · up to 20" : ""}
          </span>
          <Button
            disabled={selected.size === 0}
            onClick={() => {
              request.finish([...selected.values()]);
            }}
          >
            Choose selected
          </Button>
        </footer>
      </div>
    </div>
  );
}
