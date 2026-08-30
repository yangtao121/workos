// The Artifact Viewer client renders persisted review artifacts read-only
// and inert. Content arrives from Core exactly as stored; nothing here can
// execute, fetch, or navigate: no HTML parser, no dangerouslySetInnerHTML,
// no images, no active links, no storage, no telemetry. Every byte of
// provider content is displayed as escaped React text (ADR-0008).
import type { Artifact } from "@workos/protocol";
import { Fragment, type ReactElement } from "react";

const decoder = new TextDecoder("utf-8", { fatal: false });

export function decodeArtifactContent(content: Uint8Array): string {
  return decoder.decode(content);
}

export interface ArtifactViewerProps {
  artifact: Artifact | undefined;
  content: Uint8Array | undefined;
  loading: boolean;
  error?: string | undefined;
}

export function ArtifactViewer({ artifact, content, loading, error }: ArtifactViewerProps) {
  if (loading) {
    return <p className="empty-state">Loading artifact…</p>;
  }
  if (error) {
    return (
      <p className="empty-state" role="alert">
        Artifact unavailable.
      </p>
    );
  }
  if (!artifact || !content) {
    return <p className="empty-state">Select an artifact to review.</p>;
  }
  const text = decodeArtifactContent(content);
  if (artifact.type === "document.markdown.v1") {
    return <MarkdownView text={text} />;
  }
  if (artifact.type === "code.unified-diff.v1") {
    return <DiffView text={text} />;
  }
  // Server contract: only review subtypes are served here.
  return (
    <p className="empty-state" role="alert">
      Artifact unavailable.
    </p>
  );
}

// MarkdownView maps the bounded markdown text onto an explicit allowlist of
// React elements: headings, paragraphs, emphasis, lists, blockquotes, and
// code. Raw HTML, images, links, and every other construct degrade to plain
// escaped text — the parser below never interprets markup, it only splits
// lines and prefixes.
export function MarkdownView({ text }: { text: string }) {
  const lines = text.split("\n");
  const blocks: ReactElement[] = [];
  let index = 0;
  let key = 0;
  while (index < lines.length) {
    const line = lines[index] ?? "";
    if (line.startsWith("```")) {
      const code: string[] = [];
      index += 1;
      while (index < lines.length && !(lines[index] ?? "").startsWith("```")) {
        code.push(lines[index] ?? "");
        index += 1;
      }
      index += 1; // closing fence (or end of input)
      blocks.push(
        <pre className="md-code" key={key++}>
          <code>{code.join("\n")}</code>
        </pre>,
      );
      continue;
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      const level = heading[1]?.length ?? 1;
      const text2 = heading[2] ?? "";
      blocks.push(
        level === 1 ? (
          <h1 key={key++}>{text2}</h1>
        ) : level === 2 ? (
          <h2 key={key++}>{text2}</h2>
        ) : level === 3 ? (
          <h3 key={key++}>{text2}</h3>
        ) : (
          <h4 key={key++}>{text2}</h4>
        ),
      );
      index += 1;
      continue;
    }
    if (/^\s*([-*+]|\d+[.)])\s+/.test(line)) {
      const ordered = /^\s*\d+[.)]\s+/.test(line);
      const items: string[] = [];
      while (index < lines.length && /^\s*([-*+]|\d+[.)])\s+/.test(lines[index] ?? "")) {
        items.push((lines[index] ?? "").replace(/^\s*([-*+]|\d+[.)])\s+/, ""));
        index += 1;
      }
      blocks.push(
        ordered ? (
          <ol key={key++}>
            {items.map((item, itemIndex) => (
              <li key={itemIndex}>{renderInline(item)}</li>
            ))}
          </ol>
        ) : (
          <ul key={key++}>
            {items.map((item, itemIndex) => (
              <li key={itemIndex}>{renderInline(item)}</li>
            ))}
          </ul>
        ),
      );
      continue;
    }
    if (line.startsWith(">")) {
      const quoted: string[] = [];
      while (index < lines.length && (lines[index] ?? "").startsWith(">")) {
        quoted.push((lines[index] ?? "").replace(/^>\s?/, ""));
        index += 1;
      }
      blocks.push(<blockquote key={key++}>{renderInline(quoted.join(" "))}</blockquote>);
      continue;
    }
    if (line.trim() === "") {
      index += 1;
      continue;
    }
    const paragraph: string[] = [];
    while (index < lines.length) {
      const current = lines[index] ?? "";
      if (
        current.trim() === "" ||
        current.startsWith("#") ||
        current.startsWith(">") ||
        current.startsWith("```") ||
        /^\s*([-*+]|\d+[.)])\s+/.test(current)
      ) {
        break;
      }
      paragraph.push(current);
      index += 1;
    }
    blocks.push(<p key={key++}>{renderInline(paragraph.join(" "))}</p>);
  }
  return (
    <div className="markdown-view" aria-label="Markdown document">
      {blocks.map((block, blockIndex) => (
        <Fragment key={blockIndex}>{block}</Fragment>
      ))}
    </div>
  );
}

// renderInline maps **bold**, *emphasis*, and `code` spans onto elements.
// Everything else — URLs, HTML tags, entity soup — stays literal text.
function renderInline(line: string): ReactElement {
  const parts: ReactElement[] = [];
  const pattern = /(\*\*([^*]+)\*\*)|(\*([^*]+)\*)|(`([^`]+)`)/g;
  let cursor = 0;
  let key = 0;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(line)) !== null) {
    if (match.index > cursor) {
      parts.push(<Fragment key={key++}>{line.slice(cursor, match.index)}</Fragment>);
    }
    if (match[2] !== undefined) {
      parts.push(<strong key={key++}>{match[2]}</strong>);
    } else if (match[4] !== undefined) {
      parts.push(<em key={key++}>{match[4]}</em>);
    } else if (match[6] !== undefined) {
      parts.push(<code key={key++}>{match[6]}</code>);
    }
    cursor = match.index + match[0].length;
  }
  if (cursor < line.length) {
    parts.push(<Fragment key={key++}>{line.slice(cursor)}</Fragment>);
  }
  return <>{parts}</>;
}

export type DiffLineKind =
  | "file-header"
  | "hunk-header"
  | "addition"
  | "deletion"
  | "context"
  | "meta";

// classifyDiffLine maps one unified-diff line to its display kind. The line
// is untrusted inert text either way; the kind only picks a style class.
export function classifyDiffLine(line: string): DiffLineKind {
  if (
    line.startsWith("diff --git ") ||
    line.startsWith("index ") ||
    line.startsWith("old mode") ||
    line.startsWith("new mode") ||
    line.startsWith("similarity ") ||
    line.startsWith("rename ")
  ) {
    return "file-header";
  }
  if (line.startsWith("--- ") || line.startsWith("+++ ")) {
    return "file-header";
  }
  if (line.startsWith("@@")) {
    return "hunk-header";
  }
  if (line.startsWith("+")) {
    return "addition";
  }
  if (line.startsWith("-")) {
    return "deletion";
  }
  if (line.startsWith("\\")) {
    return "meta";
  }
  return "context";
}

// DiffView renders the unified diff as inert, line-safe text. Paths and hunk
// headers are plain strings; nothing is parsed into a patch and nothing can
// be applied.
export function DiffView({ text }: { text: string }) {
  const lines = text.split("\n");
  // A trailing newline yields one empty final segment; keep it out of the
  // line list without hiding an intentionally empty last diff line.
  if (lines.length > 0 && lines[lines.length - 1] === "") {
    lines.pop();
  }
  return (
    <div className="diff-view" aria-label="Unified diff">
      {lines.map((line, index) => (
        <div className={`diff-line diff-${classifyDiffLine(line)}`} key={index}>
          <span className="diff-line-text">{line}</span>
        </div>
      ))}
    </div>
  );
}
