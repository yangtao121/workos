// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { WorkOSClients } from "@workos/agent-sdk";
import { ExecutionQuestions } from "./ExecutionQuestions.js";
afterEach(cleanup);
it("requires an explicit choice and sends it for the exact execution question", async () => {
  const respondExecutionInteraction = vi
    .fn()
    .mockResolvedValue({ interaction: { state: "answered" } });
  const clients = {
    agentInteractions: {
      listTaskInteractions: vi.fn().mockResolvedValue({
        interactions: [
          {
            id: "question-id",
            state: "pending",
            questions: [
              {
                id: "direction",
                text: "Choose direction",
                detail: "",
                multiple: false,
                choices: [
                  { label: "Continue", description: "" },
                  { label: "Stop", description: "" },
                ],
              },
            ],
          },
        ],
      }),
      respondExecutionInteraction,
    },
  } as unknown as WorkOSClients;
  render(<ExecutionQuestions taskId="task" workosClients={clients} />);
  const submit = await screen.findByRole("button", { name: "Send answer" });
  expect((submit as HTMLButtonElement).disabled).toBe(true);
  await userEvent.click(screen.getByRole("radio", { name: "Continue" }));
  await userEvent.click(submit);
  await waitFor(() => {
    expect(respondExecutionInteraction).toHaveBeenCalledWith(
      expect.objectContaining({
        interactionId: "question-id",
        reject: false,
        answers: [{ questionId: "direction", selected: ["Continue"], text: "" }],
      }),
    );
  });
  expect(await screen.findByText("Question answered")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Send answer" })).toBeNull();
});
