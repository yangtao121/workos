import { mkdir, writeFile, symlink } from "node:fs/promises";
const response = await fetch(
  "http://127.0.0.1:8080/workos.project.v1.ProjectService/CreateProject",
  {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      name: "Files workspace",
      idempotencyKey: `files-fixture-${Date.now()}`,
    }),
  },
);
if (!response.ok) throw new Error(`project seed failed: ${response.status}`);
const { project } = await response.json();
await mkdir("/fixture/workspace", { mode: 0o700 });
await writeFile("/fixture/workspace/notes.txt", "Draft from fixture", { mode: 0o600 });
for (let i = 0; i < 20; i++)
  await writeFile(
    `/fixture/workspace/example-${String(i).padStart(2, "0")}.txt`,
    "Fixture document",
    { mode: 0o600 },
  );
await symlink("/tmp/files-outside.txt", "/fixture/workspace/outside-link");
await writeFile("/fixture/state.json", JSON.stringify({ projectId: project.id }), { mode: 0o600 });
await writeFile(
  "/fixture/config.yaml",
  `runtime:\n  workspace_mounts:\n    - owner_user_id: ${project.ownerUserId}\n      project_id: ${project.id}\n      root_path: /workspaces\n      read_only: false\n`,
  { mode: 0o644 },
);
