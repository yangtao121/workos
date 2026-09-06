// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { WorkOSClients } from "@workos/agent-sdk";
import { DocsApp, FilesApp } from "./SystemApps.js";

afterEach(cleanup);

it("loads documents beyond a page containing only patches", async () => {
  const listArtifacts = vi
    .fn()
    .mockResolvedValueOnce({
      artifacts: [{ id: "patch", type: "code.unified-diff.v1", title: "Patch" }],
      page: { nextPageToken: "next" },
    })
    .mockResolvedValueOnce({
      artifacts: [
        {
          id: "doc",
          type: "document.markdown.v1",
          title: "Project brief",
          digest: "sha256:fixture",
        },
      ],
      page: { nextPageToken: "" },
    });
  const open = vi.fn();
  render(
    <DocsApp
      projectId="project"
      workosClients={{ artifacts: { listArtifacts } } as unknown as WorkOSClients}
      onOpenArtifact={open}
    />,
  );
  await userEvent.click(await screen.findByRole("button", { name: "Load more" }));
  await userEvent.click(await screen.findByRole("button", { name: "Project brief" }));
  expect(open).toHaveBeenCalledWith(expect.objectContaining({ id: "doc", projectId: "project" }));
  expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
});

it("requests server-filtered file pages and recovers after a search failure", async () => {
  const searchHybrid = vi
    .fn()
    .mockRejectedValueOnce(new Error("private failure"))
    .mockResolvedValueOnce({ hits: [], page: {} });
  render(
    <FilesApp
      projectId="project"
      workosClients={{ index: { searchHybrid } } as unknown as WorkOSClients}
    />,
  );
  await userEvent.type(screen.getByLabelText("Search workspace files"), "notes");
  await userEvent.click(screen.getByRole("button", { name: "Search" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.queryByText("private failure")).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "Search" }));
  await waitFor(() => {
    expect(screen.getByText("No indexed files match this search.")).toBeTruthy();
  });
  expect(searchHybrid).toHaveBeenLastCalledWith(
    expect.objectContaining({ sourceType: "workspace.file.v1", query: "notes" }),
  );
});
