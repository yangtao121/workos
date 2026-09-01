import { describe, expect, it } from "vitest";
import { DeviceClass } from "@workos/protocol";
import { classifyDevice, deviceClassFromProto, protoFromDeviceClass } from "./device.js";

describe("classifyDevice", () => {
  it("keeps the stable viewport boundaries", () => {
    expect(classifyDevice(599)).toBe("phone");
    expect(classifyDevice(600)).toBe("tablet");
    expect(classifyDevice(1023)).toBe("tablet");
    expect(classifyDevice(1024)).toBe("desktop");
    expect(classifyDevice(1400)).toBe("desktop");
  });

  it("separated fold posture wins over width", () => {
    expect(classifyDevice(1280, true)).toBe("foldable");
    expect(classifyDevice(500, true)).toBe("foldable");
  });

  it("degrades a broken viewport to the phone layout", () => {
    expect(classifyDevice(Number.NaN)).toBe("phone");
    expect(classifyDevice(0)).toBe("phone");
    expect(classifyDevice(-40)).toBe("phone");
  });
});

describe("proto device class mapping", () => {
  it("round-trips every supported class", () => {
    for (const value of ["phone", "tablet", "foldable", "desktop"] as const) {
      expect(deviceClassFromProto(protoFromDeviceClass(value))).toBe(value);
    }
  });

  it("fails soft on unspecified or unknown values", () => {
    expect(deviceClassFromProto(DeviceClass.UNSPECIFIED)).toBeUndefined();
    expect(deviceClassFromProto(99 as DeviceClass)).toBeUndefined();
  });
});
