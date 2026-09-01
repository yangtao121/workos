// @vitest-environment jsdom

import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { WorkOSClients } from "@workos/agent-sdk";
import { HealthState, type GetHarnessCatalogResponse, type Project } from "@workos/protocol";
import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Desktop } from "./Desktop.js";

// React 19 act() requires this flag in jsdom to flush deferred promise updates.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const PROJECT_A = "01990000-0000-7000-8000-000000000001";
const PROJECT_B = "01990000-0000-7000-8000-000000000002";

// jsdom has no IndexedDB, so the desktop's layout store degrades to the
// in-memory adapter — deterministic for these tests.
function pinViewport(width: number, height: number) {
  Object.defineProperty(window, "innerWidth", { value: width, configurable: true });
  Object.defineProperty(window, "innerHeight", { value: height, configurable: true });
  Object.defineProperty(window, "devicePixelRatio", { value: 1, configurable: true });
}

function injectFoldSegments(posture: "side-by-side" | "stacked" | undefined) {
  if (!posture) {
    Reflect.deleteProperty(window, "getWindowSegments");
    return;
  }
  Object.defineProperty(window, "getWindowSegments", {
    configurable: true,
    value: () =>
      posture === "side-by-side"
        ? [domRect(0, 0, 632, 800), domRect(648, 0, 632, 800)]
        : [domRect(0, 0, 800, 620), domRect(0, 628, 800, 652)],
  });
}

function domRect(x: number, y: number, width: number, height: number): DOMRect {
  return {
    x,
    y,
    width,
    height,
    top: y,
    left: x,
    right: x + width,
    bottom: y + height,
    toJSON: () => undefined,
  } as DOMRect;
}

function project(id: string, name: string, revision: bigint): Project {
  return {
    $typeName: "workos.project.v1.Project",
    id,
    ownerUserId: "local-user",
    name,
    icon: "◈",
    workspaceRefs: [],
    installedAppIds: [],
    defaultAgentRole: "general",
    knowledgeCollectionId: "",
    artifactCollectionId: "",
    revision,
  };
}

function catalogFixture(): GetHarnessCatalogResponse {
  return {
    $typeName: "workos.harness.v1.GetHarnessCatalogResponse",
    providers: [
      {
        $typeName: "workos.harness.v1.HarnessProviderInfo",
        id: "fake",
        displayName: "Fake",
        adapterVersion: "1.0.0",
        health: HealthState.HEALTHY,
        unavailableReason: "",
        capabilities: {
          $typeName: "workos.harness.v1.HarnessCapabilities",
          streaming: true,
          persistentSessions: false,
          resume: false,
          steerDuringRun: false,
          approvals: false,
          toolRegistration: false,
          mcp: false,
          subagents: false,
          workspaceMount: false,
          structuredArtifacts: false,
          supportedArtifactTypes: [],
          usageReporting: true,
          hardTokenBudget: false,
          hardRuntimeDeadline: false,
          maxOutputTokens: 0n,
          maxRuntimeSeconds: 0n,
          requiresTaskCredentialLease: false,
          supportedContextRefTypes: [],
        },
      },
    ],
    defaultProviderId: "fake",
  };
}

function clientsFixture(projects: Project[]): WorkOSClients {
  return {
    projects: {
      listProjects: vi.fn(() => Promise.resolve({ projects, page: undefined })),
      getProject: vi.fn(),
      createProject: vi.fn(),
    },
    projectHarnessBindings: { setProjectHarnessBinding: vi.fn() },
    harnessCatalog: { getHarnessCatalog: vi.fn(() => Promise.resolve(catalogFixture())) },
    agentTasks: {
      submitTask: vi.fn(),
      watchTaskEvents: vi.fn(),
      getTask: vi.fn(),
    },
    appRegistry: {
      listApps: vi.fn(() => Promise.resolve({ apps: [], page: { nextPageToken: "" } })),
      getApp: vi.fn(),
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
      listArtifacts: vi.fn(() => Promise.resolve({ artifacts: [], page: { nextPageToken: "" } })),
      getReviewArtifact: vi.fn(),
    },
    surfaces: { createSurface: vi.fn(), closeSurface: vi.fn() },
    incidents: {
      listIncidents: vi.fn(() => Promise.resolve({ incidents: [], page: { nextPageToken: "" } })),
      getIncident: vi.fn(),
      acknowledgeIncident: vi.fn(),
    },
  } as unknown as WorkOSClients;
}

afterEach(() => {
  cleanup();
  window.sessionStorage.clear();
  injectFoldSegments(undefined);
});

describe("Desktop adaptive shell", () => {
  it("renders the compact phone shell with bottom navigation and single pane", async () => {
    pinViewport(390, 844);
    render(<Desktop workosClients={clientsFixture([project(PROJECT_A, "Compact P", 1n)])} />);
    expect(await screen.findByRole("navigation", { name: "WorkOS navigation" })).toBeTruthy();
    // Home view shows the project and the quick destinations.
    expect(screen.getAllByText("Compact P").length).toBeGreaterThan(0);
    expect(screen.getAllByRole("button", { name: "System Monitor" }).length).toBeGreaterThan(0);
    // The expanded free-window shell is gone: no mission-control aside, no
    // desktop dock.
    expect(screen.queryByRole("complementary")).toBeNull();
    expect(screen.queryByRole("navigation", { name: "WorkOS Dock" })).toBeNull();
  });

  it("navigates to the Agent Center and back home from the bottom nav", async () => {
    pinViewport(390, 844);
    const user = userEvent.setup();
    render(<Desktop workosClients={clientsFixture([project(PROJECT_A, "Compact P", 1n)])} />);
    await user.click(await screen.findByTestId("nav-agent"));
    // The single main pane now shows the Agent Center composer.
    expect(await screen.findByLabelText("Agent goal")).toBeTruthy();
    await user.click(screen.getByTestId("nav-home"));
    expect(screen.getByText("PROJECT SPACES")).toBeTruthy();
  });

  it("opens the App Library overlay from the bottom nav", async () => {
    pinViewport(390, 844);
    const user = userEvent.setup();
    render(<Desktop workosClients={clientsFixture([project(PROJECT_A, "Compact P", 1n)])} />);
    await user.click(await screen.findByTestId("nav-apps"));
    expect(await screen.findByText("No apps have been registered yet.")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByText("No apps have been registered yet.")).toBeNull();
  });

  it("opens the project sheet from the top bar", async () => {
    pinViewport(390, 844);
    const user = userEvent.setup();
    render(
      <Desktop
        workosClients={clientsFixture([
          project(PROJECT_A, "Compact P", 1n),
          project(PROJECT_B, "Other P", 1n),
        ])}
      />,
    );
    await user.click(await screen.findByTestId("nav-projects"));
    const dialog = await screen.findByRole("dialog", { name: "Projects" });
    expect(dialog).toBeTruthy();
    await user.click(within(dialog).getByText("Other P"));
    expect(screen.queryByRole("dialog", { name: "Projects" })).toBeNull();
    expect(screen.getAllByText("Other P").length).toBeGreaterThan(0);
  });

  it("renders the medium shell with an Agent slide-over and a hidden dock", async () => {
    pinViewport(820, 1180);
    const user = userEvent.setup();
    render(<Desktop workosClients={clientsFixture([project(PROJECT_A, "Medium P", 1n)])} />);
    expect(await screen.findByTestId("open-agent-slideover")).toBeTruthy();
    expect(screen.queryByTestId("adaptive-bottom-nav")).toBeNull();
    // The slide-over opens with the Agent Center body and Escape closes it.
    await user.click(screen.getByTestId("open-agent-slideover"));
    expect(await screen.findByTestId("agent-slideover")).toBeTruthy();
    expect(screen.getByLabelText("Agent goal")).toBeTruthy();
    await user.keyboard("{Escape}");
    expect(screen.queryByTestId("agent-slideover")).toBeNull();
    // The dock is explicit: hidden until revealed, hidden again after use.
    expect(screen.queryByTestId("adaptive-dock")).toBeNull();
    await user.click(screen.getByTestId("toggle-dock"));
    expect(screen.getByTestId("adaptive-dock")).toBeTruthy();
    await user.click(
      within(screen.getByTestId("adaptive-dock")).getByRole("button", { name: "System Monitor" }),
    );
    expect(screen.queryByTestId("adaptive-dock")).toBeNull();
  });

  it("uses two fold panes only when segments exist, with a persisted single-pane preference", async () => {
    pinViewport(1280, 800);
    const user = userEvent.setup();
    injectFoldSegments("side-by-side");
    render(<Desktop workosClients={clientsFixture([project(PROJECT_B, "Fold P", 1n)])} />);
    // Two segments: the dual-pane default applies.
    expect(await screen.findByTestId("fold-pane-main")).toBeTruthy();
    expect(screen.getByTestId("fold-pane-secondary")).toBeTruthy();
    // The user can drop to a single pane; the choice is the layout
    // preference, not a forced split.
    await user.click(screen.getByTestId("toggle-panes"));
    expect(screen.getByTestId("toggle-panes").textContent).toContain("Dual pane");
    expect(screen.queryByTestId("fold-pane-secondary")).toBeNull();
  });

  it("stacks fold panes around a horizontal hinge using segment proportions", async () => {
    pinViewport(800, 1280);
    injectFoldSegments("stacked");
    render(<Desktop workosClients={clientsFixture([project(PROJECT_A, "Fold P", 1n)])} />);
    const panes = await screen.findByTestId("fold-pane-main");
    const grid = panes.parentElement;
    expect(grid?.getAttribute("data-hinge-orientation")).toBe("horizontal");
    expect(grid?.style.gridTemplateRows).toBe("620fr 8px 652fr");
    expect(grid?.style.gridTemplateColumns).toBe("minmax(0, 1fr)");
  });

  it("falls back to the medium layout when a foldable reports no segments", async () => {
    pinViewport(900, 800);
    render(<Desktop workosClients={clientsFixture([project(PROJECT_A, "Fold P", 1n)])} />);
    // No segment API: 900px wide foldable hardware degrades to medium.
    expect(await screen.findByTestId("open-agent-slideover")).toBeTruthy();
    expect(screen.queryByTestId("fold-pane-main")).toBeNull();
  });

  it("keeps the expanded desktop unchanged at desktop widths", async () => {
    pinViewport(1440, 900);
    render(<Desktop workosClients={clientsFixture([project(PROJECT_A, "Wide P", 1n)])} />);
    expect(await screen.findByText("PROJECT SPACES")).toBeTruthy();
    expect(screen.queryByTestId("adaptive-bottom-nav")).toBeNull();
    expect(screen.getByRole("button", { name: "Open System Monitor" })).toBeTruthy();
  });

  it("responds to a viewport resize between expanded and compact", async () => {
    pinViewport(1440, 900);
    render(<Desktop workosClients={clientsFixture([project(PROJECT_A, "Resize P", 1n)])} />);
    expect(await screen.findByText("PROJECT SPACES")).toBeTruthy();
    await act(async () => {
      pinViewport(390, 844);
      window.dispatchEvent(new Event("resize"));
      await new Promise((resolve) => setTimeout(resolve, 40));
    });
    expect(screen.findByTestId("adaptive-bottom-nav")).toBeTruthy();
    expect(screen.queryByRole("complementary")).toBeNull();
  });
});
