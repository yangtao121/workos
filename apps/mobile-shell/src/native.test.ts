import { describe, expect, it, vi } from "vitest";

import { createDeviceKeyStore, registerPushToken } from "./native.js";

describe("device key store", () => {
  it("reports an honest insecure status on the web runtime and never claims protection", async () => {
    const store = createDeviceKeyStore();
    const status = await store.status();
    expect(status.secure).toBe(false);
    expect(status.reason).toContain("ephemeral");
    await store.store("device-secret");
    expect(await store.load()).toBe("device-secret");
    await store.clear();
    expect(await store.load()).toBeUndefined();
  });
});

describe("push token registration", () => {
  it("posts the token and device id to the relay and reports success", async () => {
    let capturedBody = "";
    const fetchImpl = vi.fn((_input: string | URL, init?: RequestInit) => {
      const body: unknown = init?.body;
      capturedBody = typeof body === "string" ? body : "";
      return Promise.resolve(new Response(null, { status: 200 }));
    });
    const result = await registerPushToken({
      relayEndpoint: "https://push.example/register",
      token: "native-token",
      deviceId: "01999999-9999-7999-8999-000000000ea1",
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });
    expect(result.registered).toBe(true);
    expect(JSON.parse(capturedBody)).toEqual({
      token: "native-token",
      deviceId: "01999999-9999-7999-8999-000000000ea1",
    });
  });

  it("rejects non-https non-loopback relay endpoints before any network call", async () => {
    const fetchImpl = vi.fn();
    const result = await registerPushToken({
      relayEndpoint: "http://insecure.example/register",
      token: "native-token",
      deviceId: "device",
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });
    expect(result.registered).toBe(false);
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("reports a relay rejection with the sanitized status", async () => {
    const fetchImpl = vi.fn(() => Promise.resolve(new Response(null, { status: 503 })));
    const result = await registerPushToken({
      relayEndpoint: "https://push.example/register",
      token: "native-token",
      deviceId: "device",
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });
    expect(result.registered).toBe(false);
    expect(result.detail).toContain("503");
  });
});
