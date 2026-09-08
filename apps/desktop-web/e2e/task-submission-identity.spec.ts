import { expect, test } from "@playwright/test";

test("task submission keys bind input and reject private incident provenance", async ({
  request,
}) => {
  const stamp = crypto.randomUUID();
  const created = await request.post("/workos.project.v1.ProjectService/CreateProject", {
    data: { idempotencyKey: `identity-project-${stamp}`, name: "Task identity fixture" },
  });
  expect(created.ok()).toBe(true);
  const { project } = (await created.json()) as { project: { id: string } };
  const input = (goal: string) => ({
    targetScope: { projectId: project.id },
    role: "general",
    goal,
  });
  const key = `identity-task-${stamp}`;
  const submit = (goal: string) =>
    request.post("/workos.agent.v1.AgentTaskService/SubmitTask", {
      data: { idempotencyKey: key, input: input(goal) },
    });
  const responses = await Promise.all([submit("Synthetic repair A"), submit("Synthetic repair B")]);
  expect(responses.map((response) => response.status()).sort()).toEqual([200, 409]);
  const success = responses.find((response) => response.ok());
  const conflict = responses.find((response) => !response.ok());
  if (!success || !conflict)
    throw new Error("task race did not produce one winner and one conflict");
  const winner = (await success.json()) as {
    task: { id: string; providerId: string; input: { goal: string } };
  };
  expect(((await conflict.json()) as { code: string }).code).toBe("aborted");
  const replay = await submit(winner.task.input.goal);
  expect(replay.ok()).toBe(true);
  const replayed = (await replay.json()) as { task: { id: string; providerId: string } };
  expect(replayed.task.id).toBe(winner.task.id);
  expect(replayed.task.providerId).toBe(winner.task.providerId);
  const forged = await request.post("/workos.agent.v1.AgentTaskService/SubmitTask", {
    data: {
      idempotencyKey: `identity-forged-${stamp}`,
      input: {
        ...input("Synthetic private provenance"),
        incidentId: "0198d7ea-2110-7c42-b659-c5e4d73bc331",
      },
    },
  });
  expect(((await forged.json()) as { code: string }).code).toBe("invalid_argument");
  const listed = await request.post("/workos.agent.v1.AgentTaskService/ListTasks", {
    data: { projectId: project.id, page: { pageSize: 100 } },
  });
  expect(listed.ok()).toBe(true);
  const body = (await listed.json()) as { tasks: Array<{ id: string }> };
  expect(body.tasks.map((task) => task.id)).toEqual([winner.task.id]);
});
