// @vitest-environment jsdom
import { useWorkspaceFilePicker } from "./WorkspaceFilePicker.js";
import type { AppBridgeTransport } from "@workos/app-host";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { BridgeFileRef } from "@workos/surface-sdk";

afterEach(cleanup);
const ref: BridgeFileRef = {
  projectId: "0198d7ea-2110-7c42-b659-c5e4d73bc352",
  path: "notes.txt",
  etag: `sha256:${"a".repeat(64)}`,
};
function Harness({
  transport,
  signal,
  resolve,
  reject,
}: {
  transport: AppBridgeTransport;
  signal: AbortSignal;
  resolve: (refs: BridgeFileRef[]) => void;
  reject: (error: unknown) => void;
}) {
  const picker = useWorkspaceFilePicker();
  return (
    <>
      <button
        onClick={() => {
          void picker.open(transport, { multiple: true }, signal).then(resolve, reject);
        }}
      >
        Pick files
      </button>
      {picker.dialog}
    </>
  );
}
it("retries a directory read, returns exact references and restores focus", async () => {
  const listFiles = vi
    .fn()
    .mockRejectedValueOnce(new Error("offline"))
    .mockResolvedValue({ entries: [{ ref, directory: false, sizeBytes: 7 }], nextAfter: "" });
  const resolve = vi.fn();
  const reject = vi.fn();
  const controller = new AbortController();
  render(
    <Harness
      transport={{ listFiles } as unknown as AppBridgeTransport}
      signal={controller.signal}
      resolve={resolve}
      reject={reject}
    />,
  );
  const user = userEvent.setup();
  const trigger = screen.getByRole("button", { name: "Pick files" });
  await user.click(trigger);
  await screen.findByText("Files could not be loaded.");
  await user.click(screen.getByRole("button", { name: "Retry" }));
  await user.click(await screen.findByRole("checkbox", { name: "notes.txt 7 B" }));
  await user.click(screen.getByRole("button", { name: "Choose selected" }));
  expect(resolve).toHaveBeenCalledWith([ref]);
  expect(reject).not.toHaveBeenCalled();
  expect(document.activeElement).toBe(trigger);
});
it("cancels the selector and ignores a late directory response when the surface closes", async () => {
  let finish!: () => void;
  const listFiles = vi.fn(
    () =>
      new Promise((resolve) => {
        finish = () => {
          resolve({ entries: [{ ref, directory: false, sizeBytes: 7 }], nextAfter: "" });
        };
      }),
  );
  const resolve = vi.fn();
  const reject = vi.fn();
  const controller = new AbortController();
  render(
    <Harness
      transport={{ listFiles } as unknown as AppBridgeTransport}
      signal={controller.signal}
      resolve={resolve}
      reject={reject}
    />,
  );
  await userEvent.setup().click(screen.getByRole("button", { name: "Pick files" }));
  await screen.findByRole("dialog");
  controller.abort();
  await waitFor(() => {
    expect(screen.queryByRole("dialog")).toBeNull();
  });
  finish();
  await waitFor(() => {
    expect(reject).toHaveBeenCalledOnce();
  });
  expect(resolve).not.toHaveBeenCalled();
});
