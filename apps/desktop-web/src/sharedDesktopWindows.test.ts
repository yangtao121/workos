// @vitest-environment jsdom
import { create } from "@bufbuild/protobuf";
import { DesktopWindowSchema, SurfaceSessionSchema } from "@workos/protocol";
import { expect, it } from "vitest";
import { projectSharedWindow, targetForWindow } from "./sharedDesktopWindows.js";

it("publishes only canonical app/workload references, never a device surface or capability", () => {
  const target = targetForWindow(
    {
      kind: "app-surface",
      appId: "installed-app",
      expectedWorkloadId: "program",
      expectedWorkloadGeneration: 4n,
      surface: {
        projectId: "project",
        surfaceSessionId: "device-surface",
        url: "/surfaces/device-only/",
      },
    },
    "project",
  );
  expect(target.resource).toEqual({ case: "appInstanceId", value: "installed-app" });
  expect(target.expectedWorkloadId).toBe("program");
  expect(target.expectedWorkloadGeneration).toBe(4n);
  expect(Object.keys(target)).not.toContain("surface");
  expect(Object.values(target)).not.toContain("device-surface");
});
it("projects a selected session without a local surface and translates declarative rendering", () => {
  const selected = projectSharedWindow(
    create(DesktopWindowSchema, {
      id: "logical",
      target: {
        kind: "agent-sessions",
        projectId: "project",
        resource: { case: "sessionId", value: "selected-session" },
      },
    }),
  );
  expect(selected?.sessionId).toBe("selected-session");
  const app = projectSharedWindow(
    create(DesktopWindowSchema, {
      id: "logical-app",
      target: {
        kind: "app-surface",
        projectId: "project",
        resource: { case: "appInstanceId", value: "app" },
      },
    }),
    create(SurfaceSessionSchema, {
      id: "device",
      renderer: 3,
      url: "/surfaces/device/",
      projectId: "project",
    }),
  );
  expect(app?.surface?.renderer).toBe("declarative");
});
