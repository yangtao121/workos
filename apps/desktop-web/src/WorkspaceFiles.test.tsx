// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { WorkOSClients } from "@workos/agent-sdk";
import { WorkspaceFiles } from "./WorkspaceFiles.js";
afterEach(cleanup);
it("preserves a draft after a conflicting save and submits the observed revision and etag", async () => {
  const writeWorkspaceFile = vi.fn().mockRejectedValue(new Error("stale"));
  const clients = {
    projectWorkspaces: {
      listWorkspaceFiles: vi
        .fn()
        .mockResolvedValue({ entries: [{ path: "hello.txt", kind: "file", size: 3n }] }),
      readWorkspaceFile: vi.fn().mockResolvedValue({
        content: "old",
        etag: "sha256:old",
        workspaceRevision: 4n,
        readOnly: false,
      }),
      writeWorkspaceFile,
    },
  } as unknown as WorkOSClients;
  render(<WorkspaceFiles projectId="project" workosClients={clients} />);
  await userEvent.click(await screen.findByRole("button", { name: "hello.txt" }));
  const editor = await screen.findByRole("textbox", { name: "File content" });
  await userEvent.clear(editor);
  await userEvent.type(editor, "my draft");
  await userEvent.click(screen.getByRole("button", { name: "Save file" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect((editor as HTMLTextAreaElement).value).toBe("my draft");
  expect(writeWorkspaceFile).toHaveBeenCalledWith({
    projectId: "project",
    path: "hello.txt",
    content: "my draft",
    expectedEtag: "sha256:old",
    workspaceRevision: 4n,
  });
});
it("does not offer writes for a read-only workspace", async () => {
  const writeWorkspaceFile = vi.fn();
  const clients = {
    projectWorkspaces: {
      listWorkspaceFiles: vi
        .fn()
        .mockResolvedValue({ entries: [{ path: "readme.md", kind: "file", size: 3n }] }),
      readWorkspaceFile: vi.fn().mockResolvedValue({
        content: "readonly",
        etag: "version",
        workspaceRevision: 2n,
        readOnly: true,
      }),
      writeWorkspaceFile,
    },
  } as unknown as WorkOSClients;
  render(<WorkspaceFiles projectId="project" workosClients={clients} />);
  await userEvent.click(await screen.findByRole("button", { name: "readme.md" }));
  await waitFor(() => {
    expect(screen.getByRole("textbox", { name: "File content" }).hasAttribute("readonly")).toBe(
      true,
    );
  });
  expect(screen.getByRole("button", { name: "Save file" }).hasAttribute("disabled")).toBe(true);
  expect(writeWorkspaceFile).not.toHaveBeenCalled();
});
