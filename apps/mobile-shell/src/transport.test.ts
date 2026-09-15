import { afterEach, describe, expect, it, vi } from "vitest";
import type * as CapacitorModule from "@capacitor/core";
import { Capacitor, CapacitorHttp } from "@capacitor/core";
import { createClient } from "@connectrpc/connect";
import { DeviceService } from "@workos/protocol";
import { createMobileTransport } from "./transport.js";

vi.mock("@capacitor/core", async (importOriginal) => {
  const actual = await importOriginal<typeof CapacitorModule>();
  return { ...actual, CapacitorHttp: { request: vi.fn() } };
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.clearAllMocks();
});
describe("native unary transport", () => {
  it("uses the native HTTP jar and configured Origin with redirects disabled", async () => {
    vi.spyOn(Capacitor, "isNativePlatform").mockReturnValue(true);
    const request = vi.spyOn(CapacitorHttp, "request").mockResolvedValue({
      data: { device: { deviceId: "0198d7ea-2110-7c42-b659-c5e4d73bc301" } },
      status: 200,
      headers: { "Content-Type": "application/json" },
      url: "https://gateway.example",
    });
    const client = createClient(DeviceService, createMobileTransport("https://gateway.example"));
    expect((await client.getCurrentDevice({})).device?.deviceId).toBe(
      "0198d7ea-2110-7c42-b659-c5e4d73bc301",
    );
    expect(request.mock.calls[0]?.[0].headers?.Origin).toBe("https://gateway.example");
    expect(request.mock.calls[0]?.[0].disableRedirects).toBe(true);
    expect(request.mock.calls[0]?.[0].method).toBe("POST");
    expect(request.mock.calls[0]?.[0].data).toEqual({});
  });
  it("refuses insecure origins and redirect responses", async () => {
    vi.spyOn(Capacitor, "isNativePlatform").mockReturnValue(true);
    const request = vi
      .spyOn(CapacitorHttp, "request")
      .mockResolvedValue({ data: "", status: 302, headers: {}, url: "https://elsewhere.example" });
    await expect(
      createClient(DeviceService, createMobileTransport("http://gateway.example")).getCurrentDevice(
        {},
      ),
    ).rejects.toThrow();
    expect(request).not.toHaveBeenCalled();
    await expect(
      createClient(
        DeviceService,
        createMobileTransport("https://gateway.example"),
      ).getCurrentDevice({}),
    ).rejects.toThrow("redirect refused");
  });
});
