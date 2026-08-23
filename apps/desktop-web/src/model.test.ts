import { describe, expect, it } from "vitest";
import { AgentTaskState } from "@workos/protocol";
import { taskStatus } from "./model.js";

describe("taskStatus", () => {
  it("presents canonical task states without transport prefixes", () => {
    expect(taskStatus(AgentTaskState.COMPLETED)).toBe("completed");
  });
});
