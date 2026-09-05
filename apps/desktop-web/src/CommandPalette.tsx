// Command Palette (W6): one bounded keyboard surface over a fixed action
// set. There is no arbitrary command execution — every action is a shell
// action that revalidates its target through an existing public service
// before running, and a revalidated-stale target renders the fixed stale
// verdict instead of falling back to anything else.
import { useEffect, useMemo, useRef, useState } from "react";

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
  const [stale, setStale] = useState(false);
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
    inputRef.current?.focus();
  }, []);

  useEffect(() => {
    setCursor(0);
    setStale(false);
  }, [query]);

  const runAction = async (action: PaletteAction) => {
    if (busy) return;
    setBusy(true);
    try {
      const verdict = await action.run();
      if (verdict === "stale") {
        // Fixed stale copy: no fallback navigation, no invented target.
        setStale(true);
      } else {
        onClose();
      }
    } finally {
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
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            onClose();
          } else if (event.key === "ArrowDown") {
            event.preventDefault();
            setCursor((value) => Math.min(value + 1, results.length - 1));
          } else if (event.key === "ArrowUp") {
            event.preventDefault();
            setCursor((value) => Math.max(value - 1, 0));
          } else if (event.key === "Enter" && results[cursor]) {
            event.preventDefault();
            void runAction(results[cursor]);
          }
        }}
      >
        <input
          ref={inputRef}
          aria-label="Search commands"
          className="palette-input"
          placeholder="Type a command…"
          value={query}
          maxLength={128}
          onChange={(event) => {
            setQuery(event.target.value);
          }}
        />
        {stale ? (
          <p className="palette-stale" role="status">
            This action is no longer available.
          </p>
        ) : null}
        <ul className="palette-results" role="listbox">
          {results.map((action, index) => (
            <li key={action.id} role="option" aria-selected={index === cursor}>
              <button
                type="button"
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
      </div>
    </div>
  );
}
