import type { Transport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { DeviceAuthClient } from "./index.js";

describe("pairing storage prerequisite", () => {
  it("does not claim a ticket when IndexedDB is unavailable", async () => {
    let networkCalls = 0;
    const transport = {
      unary: () => {
        networkCalls += 1;
        throw new Error("network must not be reached");
      },
      stream: () => {
        networkCalls += 1;
        throw new Error("network must not be reached");
      },
    } as unknown as Transport;
    const client = new DeviceAuthClient("https://workos.example", transport);

    await expect(
      client.pairWithTicket({
        secret: "A".repeat(43),
        tlsFingerprint: `sha256:${"ab".repeat(32)}`,
        deviceName: "Storage failure fixture",
        deviceClass: "desktop",
      }),
    ).rejects.toThrow("IndexedDB is unavailable");
    expect(networkCalls).toBe(0);
  });
});
