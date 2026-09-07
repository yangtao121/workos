// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { WorkOSClients } from "@workos/agent-sdk";
import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { KnowledgeCenter } from "./KnowledgeCenter.js";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const artifactId = "01990000-0000-7000-8000-000000000001";
const digest = `sha256:${"a".repeat(64)}`;

afterEach(cleanup);

function response(overrides: Record<string, unknown> = {}) {
  return {
    hits: [
      {
        contextRef: `artifact.review.v1:${artifactId}:${digest}`,
        artifactId,
        artifactType: "document.markdown.v1",
        digest,
        title: "Safe review title",
        excerpt: "A deterministic safe excerpt.",
        score: 0.5,
        sourceRef: { type: "artifact.review.v1", id: artifactId, revision: digest },
        ...overrides,
      },
    ],
    page: { nextPageToken: "" },
    freshness: { caughtUp: true },
  };
}

function clients(search: ReturnType<typeof vi.fn>): WorkOSClients {
  return { index: { searchHybrid: search } } as unknown as WorkOSClients;
}

function renderCenter(search: ReturnType<typeof vi.fn>) {
  render(
    <KnowledgeCenter
      projectId="01990000-0000-7000-8000-000000000002"
      workosClients={clients(search)}
      onUseAsContext={vi.fn()}
      onOpenArtifact={vi.fn()}
    />,
  );
}

describe("KnowledgeCenter response and generation boundary", () => {
  it("drops a late response after the query changes", async () => {
    let resolveSearch: (value: ReturnType<typeof response>) => void = () => undefined;
    const pending = new Promise<ReturnType<typeof response>>((resolve) => {
      resolveSearch = resolve;
    });
    const search = vi.fn(() => pending);
    const user = userEvent.setup();
    renderCenter(search);

    const input = screen.getByTestId<HTMLInputElement>("knowledge-search-input");
    await user.type(input, "first query");
    await user.click(screen.getByTestId("knowledge-search-submit"));
    expect(search).toHaveBeenCalledTimes(1);

    await user.clear(input);
    await user.type(input, "second query");
    await act(async () => {
      resolveSearch(response());
      await pending;
    });

    expect(screen.queryByTestId("knowledge-result")).toBeNull();
    expect(
      screen.getByText("Search review documents and indexed workspace files in this project."),
    ).toBeTruthy();
  });

  it("fails the whole page closed when typed and legacy refs drift", async () => {
    const search = vi.fn(() =>
      Promise.resolve(
        response({ contextRef: `artifact.review.v1:${artifactId}:sha256:${"b".repeat(64)}` }),
      ),
    );
    const user = userEvent.setup();
    renderCenter(search);

    await user.type(screen.getByTestId("knowledge-search-input"), "safe query");
    await user.click(screen.getByTestId("knowledge-search-submit"));

    expect((await screen.findByTestId("knowledge-unavailable")).textContent).toContain(
      "Knowledge search could not be loaded.",
    );
    expect(screen.queryByTestId("knowledge-result")).toBeNull();
  });
});

it("renders mixed sources and opens workspace previews without adding invalid Agent context", async () => {
  const fileId = "01990000-0000-7000-8000-000000000003";
  const file = {
    ...response().hits[0],
    artifactId: fileId,
    artifactType: "workspace.text.v1",
    title: "notes.md",
    sourceRef: { type: "workspace.file.v1", id: fileId, revision: digest },
    contextRef: `workspace.file.v1:${fileId}:${digest}`,
  };
  const search = vi.fn(() => Promise.resolve({ ...response(), hits: [response().hits[0], file] }));
  const readDocument = vi.fn(() =>
    Promise.resolve({
      source: file.sourceRef,
      title: "notes.md",
      content: "# Project notes\n\nA real indexed snapshot.",
    }),
  );
  const useContext = vi.fn();
  const openArtifact = vi.fn();
  render(
    <KnowledgeCenter
      projectId="01990000-0000-7000-8000-000000000002"
      workosClients={{ index: { searchHybrid: search, readDocument } } as unknown as WorkOSClients}
      onUseAsContext={useContext}
      onOpenArtifact={openArtifact}
    />,
  );
  await userEvent.type(screen.getByTestId("knowledge-search-input"), "project");
  await userEvent.click(screen.getByTestId("knowledge-search-submit"));
  expect(await screen.findAllByTestId("knowledge-result")).toHaveLength(2);
  expect(screen.getAllByTestId("knowledge-use-as-context")).toHaveLength(1);
  await userEvent.click(screen.getByRole("button", { name: /notes.md/ }));
  expect(await screen.findByText(/A real indexed snapshot/)).toBeTruthy();
  expect(openArtifact).not.toHaveBeenCalled();
  expect(useContext).not.toHaveBeenCalled();
});
