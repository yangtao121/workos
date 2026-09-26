import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { GreenfieldWindowFrameTileSchema } from "@workos/protocol";
import { GreenfieldFrameAssembler } from "./greenfieldWindowFrames.js";

const png = new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10, 0]);

function tile(overrides: Record<string, unknown> = {}) {
  return create(GreenfieldWindowFrameTileSchema, {
    windowId: "window-1",
    workloadGeneration: 3n,
    windowRevision: 7n,
    frameSequence: 1n,
    tileIndex: 0,
    tileCount: 2,
    x: 0,
    y: 0,
    width: 512,
    height: 512,
    frameWidth: 1024,
    frameHeight: 512,
    fullRefresh: true,
    png,
    ...overrides,
  });
}

describe("GreenfieldFrameAssembler", () => {
  it("waits for a gapless complete frame before a native window can become ready", () => {
    const frames = new GreenfieldFrameAssembler("window-1", 3n, 7n);
    expect(frames.push(tile())).toBeUndefined();
    const complete = frames.push(tile({ tileIndex: 1, x: 512 }));
    expect(complete?.tiles).toHaveLength(2);
    if (!complete) throw new Error("missing complete frame");
    frames.committed(complete);
    const delta = frames.push(
      tile({ frameSequence: 2n, tileCount: 1, width: 10, height: 10, fullRefresh: false }),
    );
    expect(delta?.fullRefresh).toBe(false);
    if (!delta) throw new Error("missing delta");
    frames.committed(delta);
    expect(frames.push(tile({ frameSequence: 2n }))).toBeUndefined();
  });

  it("rejects incomplete, overlapping, mismatched and out-of-order tiles", () => {
    expect(() =>
      new GreenfieldFrameAssembler("window-1", 3n, 7n).push(tile({ fullRefresh: false })),
    ).toThrow(/first frame/);
    const overlap = new GreenfieldFrameAssembler("window-1", 3n, 7n);
    overlap.push(tile());
    expect(() => overlap.push(tile({ tileIndex: 1, x: 500 }))).toThrow(/gap or overlap/);
    expect(() =>
      new GreenfieldFrameAssembler("window-1", 3n, 7n).push(tile({ windowRevision: 8n })),
    ).toThrow(/invalid native frame/);
    expect(() =>
      new GreenfieldFrameAssembler("window-1", 3n, 7n).push(tile({ png: new Uint8Array([1]) })),
    ).toThrow(/invalid native frame/);
  });

  it("requires full refresh after a dropped sequence or resize", () => {
    const frames = new GreenfieldFrameAssembler("window-1", 3n, 7n);
    frames.push(tile());
    const first = frames.push(tile({ tileIndex: 1, x: 512 }));
    if (!first) throw new Error("missing first frame");
    frames.committed(first);
    expect(() =>
      frames.push(
        tile({ frameSequence: 3n, tileCount: 1, width: 8, height: 8, fullRefresh: false }),
      ),
    ).toThrow(/gap requires full refresh/);
    expect(() =>
      frames.push(tile({ frameSequence: 2n, frameWidth: 2048, tileCount: 1, fullRefresh: false })),
    ).toThrow(/resize requires full refresh/);
    expect(frames.push(tile({ frameSequence: 3n }))).toBeUndefined();
    const refreshed = frames.push(tile({ frameSequence: 3n, tileIndex: 1, x: 512 }));
    expect(refreshed?.fullRefresh).toBe(true);
  });
});
