import { readFile, writeFile } from "node:fs/promises";
import { expect, test, type APIRequestContext } from "@playwright/test";

type SourceFile = { path: string; content: string; executable?: boolean };
type Bundle = {
  id: string;
  digest: string;
  files: SourceFile[];
  totalSizeBytes: string;
  createdAt: string;
};
type State = { bundle: Bundle; manifestDigest: string };
const sourceAPI = "/workos.app.v1.AppSourceBundleService/";
const registryAPI = "/workos.app.v1.AppRegistryService/";
const files: SourceFile[] = [
  {
    path: "main.go",
    content: Buffer.from(
      "package main\n\nfunc answer() int { return 42 }\nfunc main() {}\n",
    ).toString("base64"),
  },
  {
    path: "main_test.go",
    content: Buffer.from(
      'package main\n\nimport "testing"\n\nfunc TestAnswer(t *testing.T) { if answer() != 42 { t.Fatal("wrong answer") } }\n',
    ).toString("base64"),
  },
  { path: "go.mod", content: Buffer.from("module source-fixture\n\ngo 1.26\n").toString("base64") },
];
files.push({ path: "说明.md", content: Buffer.from("固定源码与测试配置\n").toString("base64") });
const statePath = () => {
  const path = process.env.WORKOS_APP_SOURCE_STATE;
  if (!path) throw new Error("run this test through make test-app-build-inputs");
  return path;
};
async function rpc<T>(request: APIRequestContext, method: string, data: object): Promise<T> {
  const response = await request.post(method, { data });
  expect(response.ok(), await response.text()).toBe(true);
  return (await response.json()) as T;
}
function manifest(
  bundle: Bundle,
  version = "1.0.0",
  testCommand = '["go", "test", "./..."]',
): string {
  return `apiVersion: workos.app/v1
id: source-build-fixture
name: Source build fixture
version: ${version}
scope: project
runtime:
  type: container
  image: localhost/source-runtime-fixture@sha256:${"a".repeat(64)}
  command: ["/app/app"]
  port: 8080
surfaces:
  - id: main
    renderer: web-service
    route: /
permissions: []
resources:
  cpuHard: 1
  memoryHighMb: 64
  memoryMaxMb: 96
  pidsMax: 32
health:
  httpPath: /health
  startupSeconds: 10
  restartLimit: 1
maintainer: {}
build:
  sourceBundleId: ${bundle.id}
  sourceDigest: ${bundle.digest}
  baseImage: localhost/source-toolchain-fixture@sha256:${"b".repeat(64)}
  buildCommand: ["go", "build", "-o", "app", "."]
  testCommand: ${testCommand}
`;
}
const encoded = (text: string) => Buffer.from(text).toString("base64");

test("source seed fixes canonical files and build recipe", async ({ request }) => {
  const { bundle } = await rpc<{ bundle: Bundle }>(request, sourceAPI + "CreateAppSourceBundle", {
    idempotencyKey: "source-key",
    files,
  });
  expect(BigInt(bundle.totalSizeBytes)).toBe(
    BigInt(files.reduce((sum, file) => sum + Buffer.from(file.content, "base64").length, 0)),
  );
  expect(bundle.id).toMatch(/^[\da-f]{8}-[\da-f]{4}-7[\da-f]{3}-[89ab][\da-f]{3}-[\da-f]{12}$/);
  expect(bundle.files.map((file) => file.path)).toEqual([
    "go.mod",
    "main.go",
    "main_test.go",
    "说明.md",
  ]);
  const replay = await rpc<{ bundle: Bundle }>(request, sourceAPI + "CreateAppSourceBundle", {
    idempotencyKey: "source-key",
    files: [...files].reverse(),
  });
  expect(replay.bundle).toEqual(bundle);
  const conflict = await request.post(sourceAPI + "CreateAppSourceBundle", {
    data: {
      idempotencyKey: "source-key",
      files: [{ ...files[0], executable: true }, ...files.slice(1)],
    },
  });
  expect(((await conflict.json()) as { code: string }).code).toBe("aborted");
  const invalid = await request.post(sourceAPI + "CreateAppSourceBundle", {
    data: {
      idempotencyKey: "invalid-source",
      files: [{ path: "../host", content: encoded("synthetic") }],
    },
  });
  expect(((await invalid.json()) as { code: string }).code).toBe("invalid_argument");
  await rpc(request, sourceAPI + "CreateAppSourceBundle", {
    idempotencyKey: "invalid-source",
    files,
  });
  const { app } = await rpc<{ app: { manifestDigest: string } }>(
    request,
    registryAPI + "RegisterApp",
    { idempotencyKey: "source-version", manifestYaml: encoded(manifest(bundle)) },
  );
  await writeFile(
    statePath(),
    JSON.stringify({ bundle, manifestDigest: app.manifestDigest } satisfies State),
  );
});

test("source restore preserves input after Core restart and rejects unbound recipes", async ({
  request,
}) => {
  const { bundle, manifestDigest } = JSON.parse(await readFile(statePath(), "utf8")) as State;
  const restored = await rpc<{ bundle: Bundle }>(request, sourceAPI + "GetAppSourceBundle", {
    bundleId: bundle.id,
  });
  expect(restored.bundle).toEqual(bundle);
  const replay = await rpc<{ bundle: Bundle }>(request, sourceAPI + "CreateAppSourceBundle", {
    idempotencyKey: "source-key",
    files,
  });
  expect(replay.bundle).toEqual(bundle);
  const registered = await rpc<{ app: { manifestDigest: string } }>(
    request,
    registryAPI + "RegisterApp",
    { idempotencyKey: "source-version", manifestYaml: encoded(manifest(bundle)) },
  );
  expect(registered.app.manifestDigest).toBe(manifestDigest);
  const wrong = { ...bundle, digest: `sha256:${"c".repeat(64)}` };
  const denied = await request.post(registryAPI + "RegisterApp", {
    data: {
      idempotencyKey: "source-next-version",
      manifestYaml: encoded(manifest(wrong, "1.0.1")),
    },
  });
  expect(((await denied.json()) as { code: string }).code).toBe("not_found");
  const next = await rpc<{ app: { manifestDigest: string } }>(
    request,
    registryAPI + "RegisterApp",
    {
      idempotencyKey: "source-next-version",
      manifestYaml: encoded(manifest(bundle, "1.0.1", '["go", "test", "-race", "./..."]')),
    },
  );
  expect(next.app.manifestDigest).not.toBe(manifestDigest);
  const coreURL = process.env.WORKOS_APP_SOURCE_CORE_URL;
  if (!coreURL) throw new Error("missing isolated Core endpoint");
  const headers = {
    "X-WorkOS-User-ID": "01999999-9999-7999-8999-000000000b03",
    "X-WorkOS-Device-ID": "01999999-9999-7999-8999-000000000b04",
  };
  const foreign = await request.post(coreURL + sourceAPI + "GetAppSourceBundle", {
    headers,
    data: { bundleId: bundle.id },
  });
  expect(((await foreign.json()) as { code: string }).code).toBe("not_found");
  const foreignRegistration = await request.post(coreURL + registryAPI + "RegisterApp", {
    headers,
    data: { idempotencyKey: "foreign-source-version", manifestYaml: encoded(manifest(bundle)) },
  });
  expect(((await foreignRegistration.json()) as { code: string }).code).toBe("not_found");
});
