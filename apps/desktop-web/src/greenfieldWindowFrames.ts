import type { GreenfieldWindowFrameTile } from "@workos/protocol";

const PNG_SIGNATURE = [137, 80, 78, 71, 13, 10, 26, 10];
const MAX_FRAME_BYTES = 24 * 1024 * 1024;

export interface GreenfieldFrame {
  readonly windowId: string;
  readonly workloadGeneration: bigint;
  readonly windowRevision: bigint;
  readonly sequence: bigint;
  readonly width: number;
  readonly height: number;
  readonly fullRefresh: boolean;
  readonly tiles: readonly GreenfieldWindowFrameTile[];
}

function validPNG(bytes: Uint8Array): boolean {
  return PNG_SIGNATURE.every((byte, index) => bytes[index] === byte);
}

function completeCoverage(
  tiles: readonly GreenfieldWindowFrameTile[],
  width: number,
  height: number,
) {
  let area = 0;
  for (let i = 0; i < tiles.length; i++) {
    const tile = tiles[i];
    if (!tile) return false;
    area += tile.width * tile.height;
    for (let j = i + 1; j < tiles.length; j++) {
      const other = tiles[j];
      if (!other) return false;
      if (
        tile.x < other.x + other.width &&
        other.x < tile.x + tile.width &&
        tile.y < other.y + other.height &&
        other.y < tile.y + tile.height
      )
        return false;
    }
  }
  return area === width * height;
}

// A stream subscription starts from an untrusted first tile. The consumer may
// paint only the complete frame returned by push(), and must call committed()
// after all PNGs decode and draw. Reconstructing a delta requires that commit.
export class GreenfieldFrameAssembler {
  private pending:
    | {
        sequence: bigint;
        tiles: Array<GreenfieldWindowFrameTile | undefined>;
        bytes: number;
        first: GreenfieldWindowFrameTile;
      }
    | undefined;
  private committedSequence = 0n;
  private hasFrame = false;
  private committedWidth = 0;
  private committedHeight = 0;

  constructor(
    readonly windowId: string,
    readonly workloadGeneration: bigint,
    readonly windowRevision: bigint,
  ) {}

  push(tile: GreenfieldWindowFrameTile): GreenfieldFrame | undefined {
    if (
      tile.windowId !== this.windowId ||
      tile.workloadGeneration !== this.workloadGeneration ||
      tile.windowRevision !== this.windowRevision ||
      tile.frameSequence < 1n ||
      tile.tileCount < 1 ||
      tile.tileCount > 64 ||
      tile.tileIndex >= tile.tileCount ||
      tile.width < 1 ||
      tile.height < 1 ||
      tile.width > 512 ||
      tile.height > 512 ||
      tile.frameWidth < 1 ||
      tile.frameHeight < 1 ||
      tile.frameWidth > 4096 ||
      tile.frameHeight > 4096 ||
      tile.x + tile.width > tile.frameWidth ||
      tile.y + tile.height > tile.frameHeight ||
      tile.png.byteLength < PNG_SIGNATURE.length ||
      tile.png.byteLength > 2 * 1024 * 1024 ||
      !validPNG(tile.png)
    )
      throw new Error("invalid native frame tile");
    if (tile.frameSequence <= this.committedSequence) return;
    if (!this.hasFrame && !tile.fullRefresh) throw new Error("native first frame is incomplete");
    if (
      this.hasFrame &&
      !tile.fullRefresh &&
      (tile.frameWidth !== this.committedWidth || tile.frameHeight !== this.committedHeight)
    )
      throw new Error("native resize requires full refresh");
    if (this.hasFrame && tile.frameSequence !== this.committedSequence + 1n && !tile.fullRefresh)
      throw new Error("native frame gap requires full refresh");
    if (this.pending && tile.frameSequence !== this.pending.sequence) {
      if (!tile.fullRefresh) throw new Error("native frame dropped without full refresh");
      this.pending = undefined;
    }
    this.pending ??= {
      sequence: tile.frameSequence,
      tiles: Array.from({ length: tile.tileCount }, () => undefined),
      bytes: 0,
      first: tile,
    };
    const pending = this.pending;
    const first = pending.first;
    if (
      tile.tileCount !== first.tileCount ||
      tile.frameWidth !== first.frameWidth ||
      tile.frameHeight !== first.frameHeight ||
      tile.fullRefresh !== first.fullRefresh ||
      pending.tiles[tile.tileIndex]
    )
      throw new Error("inconsistent native frame tiles");
    pending.bytes += tile.png.byteLength;
    if (pending.bytes > MAX_FRAME_BYTES) throw new Error("native frame too large");
    pending.tiles[tile.tileIndex] = tile;
    if (pending.tiles.some((candidate) => !candidate)) return;
    const tiles = pending.tiles as GreenfieldWindowFrameTile[];
    if (first.fullRefresh && !completeCoverage(tiles, first.frameWidth, first.frameHeight))
      throw new Error("native full frame has a gap or overlap");
    this.pending = undefined;
    return {
      windowId: this.windowId,
      workloadGeneration: this.workloadGeneration,
      windowRevision: this.windowRevision,
      sequence: first.frameSequence,
      width: first.frameWidth,
      height: first.frameHeight,
      fullRefresh: first.fullRefresh,
      tiles,
    };
  }

  committed(frame: GreenfieldFrame) {
    if (
      frame.windowId !== this.windowId ||
      frame.workloadGeneration !== this.workloadGeneration ||
      frame.windowRevision !== this.windowRevision ||
      frame.sequence <= this.committedSequence ||
      (!this.hasFrame && !frame.fullRefresh)
    )
      throw new Error("native frame commit is stale");
    this.committedSequence = frame.sequence;
    this.committedWidth = frame.width;
    this.committedHeight = frame.height;
    this.hasFrame = true;
  }
}
