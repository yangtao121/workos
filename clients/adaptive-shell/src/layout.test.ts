import { describe, expect, it } from "vitest";
import { normalizeSegments, resolveDeviceLayout } from "./layout.js";

const firstSegment = { x: 0, y: 0, width: 632, height: 800 };
const secondSegment = { x: 648, y: 0, width: 632, height: 800 };
const sideBySide = [firstSegment, secondSegment];

describe("resolveDeviceLayout", () => {
  it("maps phone/tablet/desktop viewports to their modes", () => {
    expect(resolveDeviceLayout({ viewportWidth: 390, viewportHeight: 844 }).mode).toBe("compact");
    expect(resolveDeviceLayout({ viewportWidth: 820, viewportHeight: 1180 }).mode).toBe("medium");
    expect(resolveDeviceLayout({ viewportWidth: 1440, viewportHeight: 900 }).mode).toBe("expanded");
  });

  it("derives orientation from the viewport", () => {
    expect(resolveDeviceLayout({ viewportWidth: 390, viewportHeight: 844 }).orientation).toBe(
      "portrait",
    );
    expect(resolveDeviceLayout({ viewportWidth: 844, viewportHeight: 390 }).orientation).toBe(
      "landscape",
    );
  });

  it("normalizes hostile pixel ratios", () => {
    expect(
      resolveDeviceLayout({ viewportWidth: 390, viewportHeight: 844, pixelRatio: 0 }).pixelRatio,
    ).toBe(1);
    expect(
      resolveDeviceLayout({ viewportWidth: 390, viewportHeight: 844, pixelRatio: Number.NaN })
        .pixelRatio,
    ).toBe(1);
    expect(
      resolveDeviceLayout({ viewportWidth: 390, viewportHeight: 844, pixelRatio: -2 }).pixelRatio,
    ).toBe(1);
    expect(
      resolveDeviceLayout({
        viewportWidth: 390,
        viewportHeight: 844,
        pixelRatio: Number.POSITIVE_INFINITY,
      }).pixelRatio,
    ).toBe(1);
    expect(
      resolveDeviceLayout({ viewportWidth: 390, viewportHeight: 844, pixelRatio: 2.5 }).pixelRatio,
    ).toBe(2.5);
  });

  it("enables fold-separated only with two valid segments", () => {
    const fold = resolveDeviceLayout({
      viewportWidth: 1280,
      viewportHeight: 800,
      segments: sideBySide,
    });
    expect(fold.mode).toBe("fold-separated");
    expect(fold.dualPane).toBe(true);
    expect(fold.deviceClass).toBe("foldable");
    expect(fold.hinge).toEqual({ horizontal: false, gap: 16 });
  });

  it("reads stacked segments as a horizontal hinge", () => {
    const stacked = resolveDeviceLayout({
      viewportWidth: 800,
      viewportHeight: 1280,
      segments: [
        { x: 0, y: 0, width: 800, height: 620 },
        { x: 0, y: 628, width: 800, height: 652 },
      ],
    });
    expect(stacked.mode).toBe("fold-separated");
    expect(stacked.hinge).toEqual({ horizontal: true, gap: 8 });
    expect(stacked.orientation).toBe("portrait");
  });

  it("canonicalizes reversed segment order", () => {
    expect(normalizeSegments([...sideBySide].reverse())).toEqual(normalizeSegments(sideBySide));
    const stacked = [
      { x: 0, y: 0, width: 800, height: 620 },
      { x: 0, y: 628, width: 800, height: 652 },
    ];
    expect(normalizeSegments([...stacked].reverse())).toEqual(normalizeSegments(stacked));
  });

  it("degrades a foldable without segments to medium or expanded", () => {
    expect(
      resolveDeviceLayout({ viewportWidth: 1280, viewportHeight: 800, segments: [] }).mode,
    ).toBe("expanded");
    expect(
      resolveDeviceLayout({ viewportWidth: 900, viewportHeight: 800, segments: undefined }).mode,
    ).toBe("medium");
  });

  it("ignores malformed segment shapes", () => {
    const viewport = { viewportWidth: 1280, viewportHeight: 800 };
    expect(resolveDeviceLayout({ ...viewport, segments: [firstSegment] }).mode).toBe("expanded");
    expect(resolveDeviceLayout({ ...viewport, segments: [...sideBySide, firstSegment] }).mode).toBe(
      "expanded",
    );
    expect(
      resolveDeviceLayout({
        ...viewport,
        segments: [
          { x: 0, y: 0, width: 700, height: 800 },
          { x: 648, y: 0, width: 632, height: 800 },
        ],
      }).mode,
    ).toBe("expanded");
    expect(
      resolveDeviceLayout({
        ...viewport,
        segments: [
          { x: 0, y: 0, width: Number.NaN, height: 800 },
          { x: 648, y: 0, width: 632, height: 800 },
        ],
      }).mode,
    ).toBe("expanded");
    expect(
      resolveDeviceLayout({
        ...viewport,
        segments: [
          { x: 0, y: 0, width: 0, height: 800 },
          { x: 648, y: 0, width: 632, height: 800 },
        ],
      }).mode,
    ).toBe("expanded");
  });

  it("stays stable under a resize storm", () => {
    let last = resolveDeviceLayout({ viewportWidth: 390, viewportHeight: 844 });
    for (let width = 300; width <= 1100; width += 7) {
      last = resolveDeviceLayout({ viewportWidth: width, viewportHeight: 800 });
      expect(last.mode).toBe(width <= 599 ? "compact" : width <= 1023 ? "medium" : "expanded");
    }
    // Hammering the same input yields the same output object content — the
    // shell can memoize on it.
    const again = resolveDeviceLayout({ viewportWidth: 1100, viewportHeight: 800 });
    expect(again).toEqual(last);
  });

  it("normalSegments rejects misaligned pairs", () => {
    expect(normalizeSegments(sideBySide)).toBeDefined();
    expect(normalizeSegments(undefined)).toBeUndefined();
    // Diagonal segments share neither axis: no valid hinge.
    expect(
      normalizeSegments([
        { x: 0, y: 0, width: 300, height: 300 },
        { x: 400, y: 400, width: 300, height: 300 },
      ]),
    ).toBeUndefined();
  });
});
