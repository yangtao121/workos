import { expect, test } from "@playwright/test";
import { readFile, writeFile } from "node:fs/promises";
import { openDesktopApp } from "./open-app.js";

test.use({ viewport: { width: 1440, height: 900 } });
test.setTimeout(180_000);

type Scope = {
  id: string;
  ownerUserId: string;
  source?: { type: string; id: string; revision: string };
};

test("workspace admin changes reach Files and Knowledge through the real stack", async ({
  page,
  context,
}) => {
  const scopeFile = process.env.WORKOS_WORKSPACE_SCOPE_FILE;
  if (!scopeFile) {
    test.skip(true, "requires the workspace CLI gate");
    return;
  }
  const phase = process.env.WORKOS_WORKSPACE_PHASE;
  if (phase === "seed") {
    const response = await page.request.post("/workos.project.v1.ProjectService/CreateProject", {
      data: { name: "Workspace gate", idempotencyKey: `workspace-gate-${String(Date.now())}` },
    });
    expect(response.ok()).toBeTruthy();
    const { project } = (await response.json()) as { project: Scope };
    expect(project.ownerUserId).toMatch(/^[a-f0-9-]{36}$/);
    await writeFile(scopeFile, JSON.stringify(project));
    await context.addInitScript((id) => {
      sessionStorage.setItem("workos.activeProjectId", id);
    }, project.id);
    await page.goto("/");
    await page.getByLabel("Agent goal").fill("produce a workspace gate review");
    await page.getByLabel("Markdown document").check();
    await page.getByRole("button", { name: "Run task" }).click();
    await expect(page.getByText(/completed by fake harness/).first()).toBeVisible({
      timeout: 120_000,
    });
    return;
  }
  const scope = JSON.parse(await readFile(scopeFile, "utf8")) as Scope;
  if (phase === "cleanup") {
    const current = await page.request.post("/workos.project.v1.ProjectService/GetProject", {
      data: { projectId: scope.id },
    });
    expect(current.ok()).toBeTruthy();
    const { project } = (await current.json()) as {
      project: { revision: string; archivedAt?: string };
    };
    if (!project.archivedAt) {
      const archive = await page.request.post("/workos.project.v1.ProjectService/ArchiveProject", {
        data: { projectId: scope.id, expectedRevision: project.revision },
      });
      expect(archive.ok()).toBeTruthy();
    }
    return;
  }
  await context.addInitScript((id) => {
    sessionStorage.setItem("workos.activeProjectId", id);
  }, scope.id);
  await page.goto("/");
  await page.getByTestId("open-files").click();
  const files = page.getByTestId("files-app");
  await files.getByLabel("Search workspace files").fill("deterministic synthetic output");
  await files.getByRole("button", { name: "Search", exact: true }).click();
  if (phase === "stopped") {
    await expect(files.getByText("No indexed files match this search.")).toBeVisible();
    expect(scope.source).toBeDefined();
    const read = await page.request.post("/workos.index.v1.IndexService/ReadDocument", {
      data: { projectId: scope.id, source: scope.source },
    });
    expect(read.status()).toBe(404);
    return;
  }
  await expect(files.locator(".files-hit")).toHaveCount(20);
  await files.getByRole("button", { name: "Load more files" }).click();
  await expect(files.locator(".files-hit")).toHaveCount(23);
  await expect(files.getByRole("button", { name: "Load more files" })).toHaveCount(0);
  const readResponse = page.waitForResponse((response) =>
    response.url().endsWith("workos.index.v1.IndexService/ReadDocument"),
  );
  await files.locator(".files-hit").filter({ hasText: "note-00.md" }).click();
  const read = await readResponse;
  expect(read.ok()).toBeTruthy();
  const document = (await read.json()) as { source: Scope["source"]; content: string };
  if (!document.source) throw new Error("ReadDocument omitted its source reference");
  expect(document.source.type).toBe("workspace.file.v1");
  scope.source = document.source;
  await writeFile(scopeFile, JSON.stringify(scope));
  await expect(page.getByRole("region", { name: "Indexed document preview" })).toContainText(
    "Workspace fixture 00",
  );
  expect(document.content).toContain("deterministic synthetic output");
  await page.getByRole("button", { name: "Close Files", exact: true }).click();
  await openDesktopApp(page, "knowledge-center");
  const search = page.getByTestId("knowledge-search-submit");
  await page.getByTestId("knowledge-search-input").fill("deterministic synthetic output");
  await search.click();
  const loadMore = page.getByTestId("knowledge-load-more");
  const results = page.getByTestId("knowledge-result");
  await expect(results).toHaveCount(20);
  await loadMore.click();
  await expect(results).toHaveCount(24);
  await expect(results.filter({ hasText: "Fake Harness Review Document" })).toHaveCount(1);
  const workspace = results.filter({ hasText: "note-00.md" });
  await expect(workspace).toContainText("Workspace file");
  await expect(workspace.getByTestId("knowledge-use-as-context")).toHaveCount(0);
  await workspace.locator(".knowledge-hit").click();
  await expect(page.getByRole("region", { name: "Indexed document preview" })).toContainText(
    "Workspace fixture 00",
  );
});
