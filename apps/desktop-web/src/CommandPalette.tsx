// Command Palette (W6): one bounded keyboard surface over a fixed action
// set. There is no arbitrary command execution — every action is a shell
// action that revalidates its target through an existing public service
// before running, and a revalidated-stale target renders the fixed stale
// verdict instead of falling back to anything else.
import { useEffect, useMemo, useRef, useState, useId } from "react";

export interface PaletteAction {
  id: string;
  label: string;
  hint?: string;
  run: () => Promise<"ok" | "stale">;
}

const MAX_RESULTS = 8;

export function CommandPalette(props: { actions: PaletteAction[]; onClose: () => void }) {
  const { actions, onClose } = props;
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const [message, setMessage] = useState("");
  const running = useRef(false);
  const listId = useId();
  const [busy, setBusy] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  const results = useMemo(() => {
    const needle = query.trim().toLowerCase();
    const filtered = needle
      ? actions.filter(
          (action) =>
            action.label.toLowerCase().includes(needle) ||
            (action.hint?.toLowerCase().includes(needle) ?? false),
        )
      : actions;
    return filtered.slice(0, MAX_RESULTS);
  }, [actions, query]);

  useEffect(() => {
    const previous = document.activeElement;
    inputRef.current?.focus();
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
    };
  }, []);

  useEffect(() => {
    setCursor(0);
    setMessage("");
  }, [query]);

  const runAction = async (action: PaletteAction) => {
    if (running.current) return;
    running.current = true;
    setBusy(true);
    try {
      const verdict = await action.run();
      if (verdict === "stale") {
        // Fixed stale copy: no fallback navigation, no invented target.
        setMessage("This action is no longer available.");
      } else {
        onClose();
      }
    } catch {
      setMessage("Could not complete this action. Try again.");
    } finally {
      running.current = false;
      setBusy(false);
    }
  };

  return (
    <div
      className="palette-overlay"
      data-testid="command-palette"
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        className="palette-panel"
        role="dialog"
        aria-label="Command palette"
        aria-modal="true"
        aria-busy={busy}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            onClose();
          } else if (event.key === "ArrowDown") {
            event.preventDefault();
            setCursor((value) => Math.max(0, Math.min(value + 1, results.length - 1)));
          } else if (event.key === "ArrowUp") {
            event.preventDefault();
            setCursor((value) => Math.max(value - 1, 0));
          } else if (event.key === "Tab") {
            const controls = [
              ...event.currentTarget.querySelectorAll<HTMLElement>("input,button:not(:disabled)"),
            ];
            const first = controls[0],
              last = controls.at(-1);
            if (event.shiftKey && document.activeElement === first) {
              event.preventDefault();
              last?.focus();
            } else if (!event.shiftKey && document.activeElement === last) {
              event.preventDefault();
              first?.focus();
            }
          } else if (event.key === "Enter" && results[cursor]) {
            event.preventDefault();
            void runAction(results[cursor]);
          }
        }}
      >
        <input
          ref={inputRef}
          aria-label="Search commands"
          role="combobox"
          aria-expanded="true"
          aria-controls={listId}
          aria-activedescendant={results[cursor] ? `${listId}-${String(cursor)}` : undefined}
          className="palette-input"
          placeholder="Type a command…"
          value={query}
          maxLength={128}
          onChange={(event) => {
            setQuery(event.target.value);
          }}
        />
        {message ? (
          <p className="palette-stale" role="status">
            {message}
          </p>
        ) : null}
        <ul id={listId} className="palette-results" role="listbox" aria-label="Commands">
          {results.map((action, index) => (
            <li
              id={`${listId}-${String(index)}`}
              key={action.id}
              role="option"
              aria-selected={index === cursor}
            >
              <button
                type="button"
                disabled={busy}
                className={index === cursor ? "palette-item active" : "palette-item"}
                onMouseEnter={() => {
                  setCursor(index);
                }}
                onClick={() => {
                  void runAction(action);
                }}
              >
                <span className="palette-label">{action.label}</span>
                {action.hint ? <span className="palette-hint">{action.hint}</span> : null}
              </button>
            </li>
          ))}
          {results.length === 0 ? <li className="palette-empty">No matching command.</li> : null}
        </ul>
        <footer className="palette-footer">
          <span>
            <kbd>↑ ↓</kbd>Navigate
          </span>
          <span>
            <kbd>↵</kbd>Open
          </span>
          <span>
            <kbd>esc</kbd>Close
          </span>
        </footer>
      </div>
    </div>
  );
}
