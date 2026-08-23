import { describe, expect, it } from "vitest";
import { AgentTaskState, HealthState, type Project } from "@workos/protocol";
import {
  capabilityEntries,
  healthLabel,
  providerSelectable,
  sameSelection,
  selectionFromProject,
  taskStatus,
} from "./model.js";

describe("taskStatus", () => {
  it("presents canonical task states without transport prefixes", () => {
    expect(taskStatus(AgentTaskState.COMPLETED)).toBe("completed");
  });
});

describe("harness presentation", () => {
  it("uses canonical health for selection", () => {
    expect(providerSelectable(HealthState.HEALTHY)).toBe(true);
    expect(providerSelectable(HealthState.DEGRADED)).toBe(true);
    expect(providerSelectable(HealthState.STARTING)).toBe(false);
    expect(providerSelectable(HealthState.UNAVAILABLE)).toBe(false);
    expect(providerSelectable(HealthState.UNSPECIFIED)).toBe(false);
    expect(healthLabel(HealthState.UNSPECIFIED)).toBe("unknown");
  });

  it("derives selection only from the persisted Project binding", () => {
    const unbound = { harnessBinding: undefined } as unknown as Project;
    const bound = {
      harnessBinding: { providerId: "deepseek" },
    } as unknown as Project;
    expect(selectionFromProject(unbound)).toEqual({ kind: "global" });
    expect(selectionFromProject(bound)).toEqual({
      kind: "provider",
      providerId: "deepseek",
    });
    expect(
      sameSelection(selectionFromProject(bound), {
        kind: "provider",
        providerId: "deepseek",
      }),
    ).toBe(true);
  });

  it("reports absent capabilities as unavailable", () => {
    expect(capabilityEntries(undefined)).toHaveLength(11);
    expect(capabilityEntries(undefined).every((entry) => !entry.available)).toBe(true);
  });
});
