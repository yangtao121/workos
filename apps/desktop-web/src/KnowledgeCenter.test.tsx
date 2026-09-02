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
  return { index: { search } } as unknown as WorkOSClients;
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
    expect(screen.getByText("Search the review documents this project has produced.")).toBeTruthy();
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
