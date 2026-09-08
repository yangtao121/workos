import { chmod } from "node:fs/promises";
import { expect, test } from "@playwright/test";

test("Generic CLI materializes reviews, consumes pinned context and rejects unhealthy admission", async ({
  request,
}) => {
  test.setTimeout(90_000);
  const executable = process.env.WORKOS_CLI_GATE_EXECUTABLE;
  if (!executable) throw new Error("run this test through make test-generic-cli");
  const rpc = async <T>(method: string, data: object): Promise<T> => {
    const response = await request.post(method, { data });
    expect(response.ok(), await response.text()).toBe(true);
    return (await response.json()) as T;
  };
  type Task = { id: string; providerId: string; state: string };
  type Artifact = { id: string; type: string; digest: string; sourceTaskId: string };
  const projectAPI = "/workos.project.v1.ProjectService/";
  const taskAPI = "/workos.agent.v1.AgentTaskService/";
  const artifactAPI = "/workos.artifact.v1.ArtifactService/";
  const { project } = await rpc<{ project: { id: string } }>(projectAPI + "CreateProject", {
    idempotencyKey: "generic-project",
    name: "Generic CLI fixture",
    harnessBinding: {
      providerId: "generic-cli",
      instancePolicy: "HARNESS_INSTANCE_POLICY_EPHEMERAL",
      resourcePolicyId: "project-no-tools",
    },
  });
  const input = {
    targetScope: { projectId: project.id },
    role: "general",
    goal: "Produce deterministic review fixtures",
    outputArtifactTypes: ["document.markdown.v1", "code.unified-diff.v1"],
  };
  const submit = (key: string, payload: object = input) =>
    rpc<{ task: Task }>(taskAPI + "SubmitTask", { idempotencyKey: key, input: payload });
  const complete = async (id: string) => {
    await expect
      .poll(
        async () => {
          const { task } = await rpc<{ task: Task }>(taskAPI + "GetTask", { taskId: id });
          return task.state;
        },
        { timeout: 20_000 },
      )
      .toBe("AGENT_TASK_STATE_COMPLETED");
  };
  const list = () =>
    rpc<{ artifacts: Artifact[] }>(artifactAPI + "ListArtifacts", { projectId: project.id });
  const first = await submit("generic-first");
  expect(first.task.providerId).toBe("generic-cli");
  await complete(first.task.id);
  const firstOutputs = (await list()).artifacts;
  expect(firstOutputs).toHaveLength(2);
  expect(firstOutputs.every((artifact) => artifact.sourceTaskId === first.task.id)).toBe(true);
  const markdown = firstOutputs.find((artifact) => artifact.type === "document.markdown.v1");
  if (!markdown) throw new Error("missing markdown output");
  const secondInput = {
    ...input,
    contextRefs: [{ type: "artifact.review.v1", id: markdown.id, revision: markdown.digest }],
  };
  const second = await submit("generic-context", secondInput);
  await complete(second.task.id);
  const outputs = (await list()).artifacts;
  expect(outputs).toHaveLength(4);
  const contextOutput = outputs.find(
    (artifact) =>
      artifact.sourceTaskId === second.task.id && artifact.type === "document.markdown.v1",
  );
  if (!contextOutput) throw new Error("missing context-bound output");
  const review = await rpc<{ content: { markdown: { content: string } } }>(
    artifactAPI + "GetReviewArtifact",
    { artifactId: contextOutput.id },
  );
  const body = Buffer.from(review.content.markdown.content, "base64").toString("utf8");
  expect(body).toContain(`Context: ${markdown.id} at ${markdown.digest}`);
  expect(body).toContain("Deterministic fixture output.");
  try {
    await chmod(executable, 0o600);
    const catalog = await rpc<{ providers: Array<{ id: string; health: string }> }>(
      "/workos.harness.v1.HarnessCatalogService/GetHarnessCatalog",
      {},
    );
    expect(catalog.providers.find((provider) => provider.id === "generic-cli")?.health).toBe(
      "HEALTH_STATE_UNAVAILABLE",
    );
    const rejected = await request.post(taskAPI + "SubmitTask", {
      data: { idempotencyKey: "generic-recovered", input },
    });
    expect(rejected.status()).toBe(400);
    expect(((await rejected.json()) as { code: string }).code).toBe("failed_precondition");
    const replay = await submit("generic-first");
    expect(replay.task.id).toBe(first.task.id);
    const tasks = await rpc<{ tasks: Task[] }>(taskAPI + "ListTasks", { projectId: project.id });
    expect(tasks.tasks).toHaveLength(2);
  } finally {
    await chmod(executable, 0o700);
  }
  const recovered = await submit("generic-recovered");
  await complete(recovered.task.id);
  expect((await list()).artifacts).toHaveLength(6);
});
