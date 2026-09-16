// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { WorkOSClients } from "@workos/agent-sdk";
import { WorkspacePreviews } from "./WorkspacePreviews.js";
afterEach(cleanup);
it("opens the same isolated preview and detaches without stopping its process", async () => {
  const stopWorkspacePreview = vi.fn();
  const preview = {
    id: "preview-id",
    command: "node server.cjs",
    state: "running",
    generation: 3n,
    port: 3000,
    url: "/previews/preview-id/fixture/",
  };
  const clients = {
    workspacePreviews: {
      listProjectWorkspacePreviews: vi.fn().mockResolvedValue({ previews: [preview] }),
      stopWorkspacePreview,
    },
  } as unknown as WorkOSClients;
  const view = render(<WorkspacePreviews projectId="project" workosClients={clients} />);
  await userEvent.click(await screen.findByRole("button", { name: "Open" }));
  const frame = screen.getByTitle("Project development preview");
  expect(frame.getAttribute("src")).toBe(preview.url);
  expect(frame.getAttribute("sandbox")).toBe("allow-scripts allow-forms");
  view.unmount();
  expect(stopWorkspacePreview).not.toHaveBeenCalled();
});
it("reuses the start intent after a lost response", async () => {
  const startWorkspacePreview = vi
    .fn<(request: { idempotencyKey: string }) => Promise<never>>()
    .mockRejectedValue(new Error("lost"));
  const clients = {
    workspacePreviews: {
      listProjectWorkspacePreviews: vi.fn().mockResolvedValue({ previews: [] }),
      startWorkspacePreview,
    },
  } as unknown as WorkOSClients;
  render(<WorkspacePreviews projectId="project" workosClients={clients} />);
  await userEvent.click(await screen.findByRole("button", { name: "Start preview" }));
  await screen.findByRole("alert");
  await userEvent.click(screen.getByRole("button", { name: "Start preview" }));
  await screen.findByRole("alert");
  expect(startWorkspacePreview).toHaveBeenCalledTimes(2);
  expect(startWorkspacePreview.mock.calls[0]?.[0]?.idempotencyKey).toBe(
    startWorkspacePreview.mock.calls[1]?.[0]?.idempotencyKey,
  );
});
