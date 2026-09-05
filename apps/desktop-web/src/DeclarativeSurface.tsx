// Declarative Surface native renderer (structure §10, ADR-0016-era slice):
// a web-bundle surface whose bundle carries a versioned declarative document
// at the well-known path `surface.json` is rendered by WorkOS itself with
// inert components instead of a sandboxed iframe. The document is bounded —
// schema version, component count, text sizes, and the component vocabulary
// are all validated client-side — and unknown components degrade to a safe
// placeholder. No scripts, links, images, or storage: the doc is data.
import { useCallback, useEffect, useState } from "react";

const DOC_VERSION = "workos.declarative-surface/v1";
const MAX_DOC_BYTES = 64 * 1024;
const MAX_COMPONENTS = 128;
const MAX_TEXT_CHARS = 2_000;
export interface DeclarativeComponent {
  type: "markdown" | "progress" | "kv" | "button" | "unknown";
  text?: string;
  value?: number;
  rows?: { key: string; value: string }[];
}

export interface DeclarativeDoc {
  version: string;
  title: string;
  components: DeclarativeComponent[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function boundedText(value: unknown, max: number): string | undefined {
  if (typeof value !== "string") return undefined;
  if (value.length === 0 || value.length > max) return undefined;
  // Control characters other than newline/tab fail closed.
  for (const ch of value) {
    const code = ch.codePointAt(0) ?? 0;
    if ((code < 0x20 && ch !== "\n" && ch !== "\t") || code === 0x7f) return undefined;
  }
  return value;
}

function parseComponent(raw: unknown): DeclarativeComponent {
  if (!isRecord(raw)) return { type: "unknown" };
  const type = raw["type"];
  if (type === "markdown") {
    const text = boundedText(raw["text"], MAX_TEXT_CHARS);
    return text === undefined ? { type: "unknown" } : { type: "markdown", text };
  }
  if (type === "progress") {
    const value = raw["value"];
    if (typeof value !== "number" || value < 0 || value > 100) {
      return { type: "unknown" };
    }
    return { type: "progress", value };
  }
  if (type === "kv") {
    if (!Array.isArray(raw["rows"])) return { type: "unknown" };
    const rows = raw["rows"]
      .slice(0, 50)
      .map((row) =>
        isRecord(row) && typeof row["key"] === "string" && typeof row["value"] === "string"
          ? { key: row["key"].slice(0, 120), value: row["value"].slice(0, 512) }
          : undefined,
      )
      .filter((row): row is { key: string; value: string } => row !== undefined);
    return { type: "kv", rows };
  }
  if (type === "button") {
    const text = boundedText(raw["text"], 120);
    return text === undefined ? { type: "unknown" } : { type: "button", text };
  }
  return { type: "unknown" };
}

export function parseDeclarativeDoc(raw: string): DeclarativeDoc {
  if (new TextEncoder().encode(raw).length > MAX_DOC_BYTES) {
    throw new Error("declarative document exceeds the size budget");
  }
  const parsed: unknown = JSON.parse(raw);
  if (!isRecord(parsed) || parsed["version"] !== DOC_VERSION) {
    throw new Error("unsupported declarative document version");
  }
  const title = boundedText(parsed["title"], 120) ?? "";
  if (!Array.isArray(parsed["components"]) || parsed["components"].length > MAX_COMPONENTS) {
    throw new Error("declarative document component list is invalid");
  }
  const components = (parsed["components"] as unknown[]).map(parseComponent);
  return { version: DOC_VERSION, title, components };
}

/** Inert declarative renderer: text only, no scripts, no links, no storage. */
export function DeclarativeSurface({
  surfaceUrl,
  onOutOfDate,
}: {
  surfaceUrl: string;
  onOutOfDate?: () => void;
}) {
  const [state, setState] = useState<"loading" | "ready" | "invalid">("loading");
  const [doc, setDoc] = useState<DeclarativeDoc | undefined>(undefined);

  const load = useCallback(async () => {
    setState("loading");
    try {
      const response = await fetch(`${surfaceUrl}surface.json`, {
        credentials: "include",
        redirect: "error",
      });
      if (!response.ok) {
        setState("invalid");
        return;
      }
      const raw = await response.text();
      const parsed = parseDeclarativeDoc(raw);
      setDoc(parsed);
      setState("ready");
    } catch {
      setState("invalid");
      onOutOfDate?.();
    }
  }, [surfaceUrl, onOutOfDate]);

  useEffect(() => {
    void load();
  }, [load]);

  if (state === "loading") {
    return (
      <div className="declarative-body" role="status">
        Loading declarative surface…
      </div>
    );
  }
  if (state === "invalid" || !doc) {
    return (
      <div className="declarative-body declarative-invalid" role="alert">
        The declarative surface document is unavailable or violates the
        bounded schema.
      </div>
    );
  }
  return (
    <div className="declarative-body" aria-label="Declarative surface">
      {doc.components.map((component, index) => {
        if (component.type === "markdown") {
          return (
            <p className="declarative-markdown" key={index}>
              {component.text}
            </p>
          );
        }
        if (component.type === "progress") {
          return (
            <progress
              className="declarative-progress"
              key={index}
              max={100}
              value={component.value ?? 0}
            />
          );
        }
        if (component.type === "kv") {
          return (
            <dl className="declarative-kv" key={index}>
              {(component.rows ?? []).map((row) => (
                <div key={row.key}>
                  <dt>{row.key}</dt>
                  <dd>{row.value}</dd>
                </div>
              ))}
            </dl>
          );
        }
        if (component.type === "button") {
          return (
            <button className="declarative-button" disabled key={index} type="button">
              {component.text}
            </button>
          );
        }
        return (
          <p className="declarative-unknown" key={index}>
            [unsupported component]
          </p>
        );
      })}
    </div>
  );
}
