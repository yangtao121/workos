import { describe, expect, it, vi } from "vitest";
import type { WorkOSClients } from "@workos/agent-sdk";
import { GreenfieldInputVerdict, GreenfieldKeyAction } from "@workos/protocol";
import {
  GreenfieldWindowInputClient,
  type GreenfieldAttachment,
} from "./greenfieldWindowClient.js";

function fixture(controls = true) {
  const attachment: GreenfieldAttachment = {
    sessionId: "session-1",
    attachmentId: "attachment-1",
    workloadGeneration: 3n,
    controlGeneration: 7n,
    controls,
  };
  const sendGreenfieldWindowInput = vi.fn((request: { events: { sequence: bigint }[] }) =>
    Promise.resolve({
      verdict: GreenfieldInputVerdict.APPLIED,
      lastAppliedSequence: request.events.at(-1)?.sequence ?? 0n,
    }),
  );
  const readGreenfieldClipboard = vi.fn(() =>
    Promise.resolve({ textUtf8: new TextEncoder().encode("中文\t🙂\nsecond line") }),
  );
  const input = new GreenfieldWindowInputClient(
    {
      sendGreenfieldWindowInput,
      readGreenfieldClipboard,
    } as unknown as WorkOSClients["greenfieldWindows"],
    attachment,
  );
  return { input, attachment, sendGreenfieldWindowInput, readGreenfieldClipboard };
}

const down = {
  case: "key" as const,
  value: { action: GreenfieldKeyAction.DOWN, code: "KeyA", key: "a" },
};

describe("GreenfieldWindowInputClient", () => {
  it("shares one increasing sequence across native windows and reads real UTF-8 selection", async () => {
    const f = fixture();
    await Promise.all([f.input.send("window-a", [down]), f.input.send("window-b", [down])]);
    expect(f.sendGreenfieldWindowInput.mock.calls[0]?.[0].events[0]?.sequence).toBe(1n);
    expect(f.sendGreenfieldWindowInput.mock.calls[1]?.[0].events[0]?.sequence).toBe(2n);
    expect(await f.input.readClipboard()).toBe("中文\t🙂\nsecond line");
    expect(f.readGreenfieldClipboard).toHaveBeenCalledWith(
      expect.objectContaining({ attachmentId: "attachment-1", controlGeneration: 7n }),
    );
  });

  it("retries an ambiguous input using the same sequence and never reports a fake apply", async () => {
    const f = fixture();
    f.sendGreenfieldWindowInput.mockRejectedValueOnce(new Error("response lost"));
    await f.input.send("window-a", [down]);
    expect(f.sendGreenfieldWindowInput).toHaveBeenCalledTimes(2);
    expect(f.sendGreenfieldWindowInput.mock.calls[0]?.[0].events[0]?.sequence).toBe(1n);
    expect(f.sendGreenfieldWindowInput.mock.calls[1]?.[0].events[0]?.sequence).toBe(1n);
    f.sendGreenfieldWindowInput.mockResolvedValueOnce({
      verdict: GreenfieldInputVerdict.UNAVAILABLE,
      lastAppliedSequence: 1n,
    });
    await expect(f.input.send("window-a", [down])).rejects.toThrow(/rejected/);
    expect(f.input.canControl).toBe(false);
  });

  it("never sends observer input or an intent queued under an old control epoch", async () => {
    const observer = fixture(false);
    await expect(observer.input.send("window-a", [down])).rejects.toThrow(/control unavailable/);
    await expect(observer.input.readClipboard()).rejects.toThrow(/control unavailable/);
    expect(observer.sendGreenfieldWindowInput).not.toHaveBeenCalled();

    const controller = fixture();
    const first = controller.input.send("window-a", [down]);
    const queued = controller.input.send("window-a", [down]);
    controller.input.setControl(false, 8n);
    await expect(first).rejects.toThrow(/control unavailable/);
    await expect(queued).rejects.toThrow(/control unavailable/);
    expect(controller.sendGreenfieldWindowInput).not.toHaveBeenCalled();
  });

  it("reports stale input and refuses to revive an ambiguous sequence in the same attachment", async () => {
    const f = fixture();
    const onControlLost = vi.fn();
    const input = new GreenfieldWindowInputClient(
      {
        sendGreenfieldWindowInput: f.sendGreenfieldWindowInput,
        readGreenfieldClipboard: f.readGreenfieldClipboard,
      } as unknown as WorkOSClients["greenfieldWindows"],
      f.attachment,
      onControlLost,
    );
    f.sendGreenfieldWindowInput.mockResolvedValueOnce({
      verdict: GreenfieldInputVerdict.UNAVAILABLE,
      lastAppliedSequence: 0n,
    });
    await expect(input.send("window-a", [down])).rejects.toThrow(/rejected/);
    expect(onControlLost).toHaveBeenCalledTimes(1);
    expect(input.needsFreshAttachment).toBe(true);
    input.setControl(true, 8n);
    expect(input.canControl).toBe(false);
    await expect(input.send("window-a", [down])).rejects.toThrow(/control unavailable/);
    expect(f.sendGreenfieldWindowInput).toHaveBeenCalledTimes(1);
  });
});
