// PTY writes are capped at 16 KiB by Runtime. Keep every chunk valid UTF-8
// while preserving the exact byte order of one browser clipboard gesture.
export const MAX_TERMINAL_CLIPBOARD_BYTES = 256 * 1024;
export const MAX_TERMINAL_WRITE_BYTES = 16 * 1024;

export function terminalClipboardChunks(text: string): Uint8Array[] {
  if (!text || text.includes("\0")) throw new RangeError("clipboard has no usable text");
  const bytes = new TextEncoder().encode(text);
  if (bytes.length > MAX_TERMINAL_CLIPBOARD_BYTES)
    throw new RangeError("terminal clipboard is too large");
  const chunks: Uint8Array[] = [];
  for (let start = 0; start < bytes.length; ) {
    let end = Math.min(start + MAX_TERMINAL_WRITE_BYTES, bytes.length);
    if (end < bytes.length) {
      while (((bytes[end] ?? 0) & 0xc0) === 0x80) end--;
    }
    chunks.push(bytes.subarray(start, end));
    start = end;
  }
  return chunks;
}

export function terminalSelection(element: HTMLElement | null): string {
  const selection = window.getSelection();
  if (!element || !selection || selection.isCollapsed || selection.rangeCount !== 1) return "";
  if (!element.contains(selection.getRangeAt(0).commonAncestorContainer)) return "";
  return selection.toString();
}
