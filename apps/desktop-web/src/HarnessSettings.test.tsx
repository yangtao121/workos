// @vitest-environment jsdom

import { useState } from "react";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  HarnessInstancePolicy,
  HealthState,
  type GetHarnessCatalogResponse,
  type HarnessCapabilities,
  type HarnessProviderInfo,
  type Project,
} from "@workos/protocol";
import { afterEach, describe, expect, it, vi } from "vitest";
import { HarnessSettings, type CatalogState } from "./HarnessSettings.js";
import { selectionFromProject, type HarnessSelection } from "./model.js";

afterEach(cleanup);

describe("HarnessSettings", () => {
  it("distinguishes catalog loading, failure with retry, and an empty catalog", async () => {
    const retry = vi.fn();
    const base = settingsProps(project());
    const { rerender } = render(
      <HarnessSettings {...base} catalogState="loading" onRetry={retry} />,
    );
    expect(screen.getByRole("status").textContent).toContain("Loading provider catalog");

    rerender(
      <HarnessSettings
        {...base}
        catalogError="Provider catalog is temporarily unavailable."
        catalogState="error"
        onRetry={retry}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Retry catalog" }));
    expect(retry).toHaveBeenCalledOnce();

    rerender(
      <HarnessSettings {...base} catalog={catalog([])} catalogState="ready" onRetry={retry} />,
    );
    expect(screen.getByText("No providers were reported.")).toBeTruthy();
  });

  it("shows canonical health and every reported capability without guessing", () => {
    render(
      <HarnessSettings
        {...settingsProps(project())}
        catalog={catalog([
          provider("fake", "Fake Harness", HealthState.HEALTHY, {
            streaming: true,
            usageReporting: true,
          }),
          provider("limited", "Limited Harness", HealthState.DEGRADED),
          provider(
            "deepseek",
            "DeepSeek Harness",
            HealthState.UNAVAILABLE,
            {},
            "Provider is disabled or misconfigured",
          ),
        ])}
        catalogState="ready"
      />,
    );

    expect(screen.getByText("global default")).toBeTruthy();
    expect(screen.getByText("degraded")).toBeTruthy();
    expect(screen.getAllByText("unavailable").length).toBeGreaterThan(1);
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select DeepSeek Harness" }).disabled,
    ).toBe(true);
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select Limited Harness" }).disabled,
    ).toBe(false);
    expect(screen.getByText("Degraded providers can be selected with caution.")).toBeTruthy();

    const fakeCapabilities = within(screen.getByLabelText("Fake Harness capabilities"));
    expect(fakeCapabilities.getByText("Streaming").nextElementSibling?.textContent).toBe(
      "available",
    );
    expect(fakeCapabilities.getByText("Approvals").nextElementSibling?.textContent).toBe(
      "unavailable",
    );
    expect(fakeCapabilities.getAllByRole("listitem")).toHaveLength(11);
  });

  it("keeps an unknown current binding visible and never renders credential data", async () => {
    const current = project("retired-provider", "credential-value-that-must-not-render");
    const saved: HarnessSelection[] = [];
    render(
      <ControlledSettings
        catalog={catalog([provider("fake", "Fake Harness", HealthState.HEALTHY)])}
        catalogState="ready"
        project={current}
        onSave={(selection) => saved.push(selection)}
      />,
    );

    const unknown = screen.getByRole("radio", { name: "Current provider retired-provider" });
    expect((unknown as HTMLInputElement).checked).toBe(true);
    expect((unknown as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByText(/Not reported by the current provider catalog/)).toBeTruthy();
    expect(document.body.textContent).not.toContain("credential-value-that-must-not-render");
    expect(screen.queryByText(/credential|api key/i)).toBeNull();

    await userEvent.click(screen.getByRole("radio", { name: "Use Global Default" }));
    await userEvent.click(screen.getByRole("button", { name: "Save harness setting" }));
    expect(saved).toEqual([{ kind: "global" }]);
  });

  it("keeps Catalog outage separate from an unknown provider health", () => {
    const current = project("deepseek");
    render(
      <HarnessSettings
        {...settingsProps(current)}
        catalogError="Provider catalog is temporarily unavailable."
        catalogState="error"
      />,
    );
    expect(screen.getByText("Current binding · catalog unavailable")).toBeTruthy();
    expect(screen.queryByText("Current binding · unknown")).toBeNull();
    expect(
      screen.getByText("The saved binding is retained. You can still use Global Default."),
    ).toBeTruthy();
  });

  it("submits explicit provider and global-default selections", async () => {
    const saved: HarnessSelection[] = [];
    const providerCatalog = catalog([
      provider("fake", "Fake Harness", HealthState.HEALTHY),
      provider("deepseek", "DeepSeek Harness", HealthState.HEALTHY),
    ]);
    const { rerender } = render(
      <ControlledSettings
        catalog={providerCatalog}
        catalogState="ready"
        project={project()}
        onSave={(selection) => saved.push(selection)}
      />,
    );

    await userEvent.click(screen.getByRole("radio", { name: "Select DeepSeek Harness" }));
    await userEvent.click(screen.getByRole("button", { name: "Save harness setting" }));
    expect(saved).toEqual([{ kind: "provider", providerId: "deepseek" }]);

    rerender(
      <ControlledSettings
        catalog={providerCatalog}
        catalogState="ready"
        project={project("deepseek")}
        onSave={(selection) => saved.push(selection)}
      />,
    );
    await userEvent.click(screen.getByRole("radio", { name: "Use Global Default" }));
    await userEvent.click(screen.getByRole("button", { name: "Save harness setting" }));
    expect(saved.at(-1)).toEqual({ kind: "global" });
  });
});

function ControlledSettings({
  project: activeProject,
  catalog: activeCatalog,
  catalogState,
  onSave,
}: {
  project: Project;
  catalog: GetHarnessCatalogResponse;
  catalogState: CatalogState;
  onSave: (selection: HarnessSelection) => void;
}) {
  const [draft, setDraft] = useState(() => selectionFromProject(activeProject));
  return (
    <HarnessSettings
      catalog={activeCatalog}
      catalogState={catalogState}
      draft={draft}
      project={activeProject}
      saving={false}
      onRetry={() => undefined}
      onSave={() => {
        onSave(draft);
      }}
      onSelectionChange={setDraft}
    />
  );
}

function settingsProps(activeProject: Project) {
  return {
    project: activeProject,
    catalogState: "ready" as const,
    draft: selectionFromProject(activeProject),
    saving: false,
    onSelectionChange: () => undefined,
    onSave: () => undefined,
    onRetry: () => undefined,
  };
}

function project(providerId?: string, credentialRef = ""): Project {
  return {
    $typeName: "workos.project.v1.Project",
    id: "019c-project",
    ownerUserId: "local-user",
    name: "Catalog Project",
    icon: "◈",
    workspaceRefs: [],
    harnessBinding: providerId
      ? {
          $typeName: "workos.project.v1.HarnessBinding",
          providerId,
          instancePolicy: HarnessInstancePolicy.EPHEMERAL,
          profileId: "",
          credentialRef,
          resourcePolicyId: "project-no-tools",
        }
      : undefined,
    installedAppIds: [],
    defaultAgentRole: "general",
    knowledgeCollectionId: "",
    artifactCollectionId: "",
    revision: 1n,
  };
}

function catalog(providers: HarnessProviderInfo[]): GetHarnessCatalogResponse {
  return {
    $typeName: "workos.harness.v1.GetHarnessCatalogResponse",
    providers,
    defaultProviderId: "fake",
  };
}

function provider(
  id: string,
  displayName: string,
  health: HealthState,
  enabled: Partial<HarnessCapabilities> = {},
  unavailableReason = "",
): HarnessProviderInfo {
  return {
    $typeName: "workos.harness.v1.HarnessProviderInfo",
    id,
    displayName,
    adapterVersion: "1.0.0",
    health,
    unavailableReason,
    capabilities: {
      $typeName: "workos.harness.v1.HarnessCapabilities",
      streaming: false,
      persistentSessions: false,
      resume: false,
      steerDuringRun: false,
      approvals: false,
      toolRegistration: false,
      mcp: false,
      subagents: false,
      workspaceMount: false,
      structuredArtifacts: false,
      usageReporting: false,
      ...enabled,
    },
  };
}
