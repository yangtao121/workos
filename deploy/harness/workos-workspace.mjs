// Backend for the official fs/shell capability seams. Native tools, policy,
// observations and the agent loop remain upstream. No host filesystem IO.
export const name = "workos-workspace";
export function apply(ctx) {
  const call = async (operation, args, signal) => {
    if (signal?.aborted) throw new Error("Workspace operation aborted");
    const result = await globalThis.__workosCall(operation, args);
    if (result.error) {
      const error = new Error(result.error);
      error.name = "FsError";
      error.code = result.error;
      throw error;
    }
    return result;
  };
  const p = (target) => target.displayPath;
  const resolve = (name, options = {}) => {
    const path = name.startsWith("/") ? name : (options.cwd || "/workspace") + "/" + name;
    return call("fs.resolve", { path }, options.signal);
  };
  ctx.provide("fs", {
    resolve,
    processPath: p,
    fileUrl: (target) => "file://" + p(target).split("/").map(encodeURIComponent).join("/"),
    contains: (parent, child) => p(parent) === p(child) || p(child).startsWith(p(parent) + "/"),
    async stat(target, signal) {
      const result = await call("fs.stat", { path: p(target) }, signal);
      return result.absent ? undefined : result;
    },
    async lstat(name, options, signal) {
      return this.stat(await resolve(name, options), signal);
    },
    async readText(target, signal) {
      return (await call("fs.read", { path: p(target) }, signal)).content;
    },
    async *streamText(target, signal) {
      yield await this.readText(target, signal);
    },
    async readBytes(target, signal, maxBytes) {
      const result = await call("fs.read", { path: p(target), encoding: "base64" }, signal);
      const data = Buffer.from(result.content, "base64");
      if (maxBytes !== undefined && data.length > maxBytes)
        throw Object.assign(new Error("FS_TOO_LARGE"), { code: "FS_TOO_LARGE" });
      return data;
    },
    async listDir(target, signal) {
      return (await call("fs.list", { path: p(target) }, signal)).entries;
    },
    writeText(target, content, expected, signal) {
      return call(
        "fs.write",
        { path: p(target), content, guard: expected?.kind || "", version: expected?.version || "" },
        signal,
      );
    },
    editText(target, edit, expected, signal) {
      return call(
        "fs.edit",
        { path: p(target), ...edit, version: expected?.version || "" },
        signal,
      );
    },
  });
  ctx.provide("shell", {
    sandboxMode: "workspace-write",
    resolve(request) {
      if (request.sandboxPolicy?.mode === "danger-full-access")
        throw new Error("Sandbox escalation unavailable");
      return {
        ...request,
        workdir: request.workdir || "/workspace",
        timeoutMs: Math.min(request.timeoutMs || 120000, 300000),
        stdoutMaxBytes: Math.min(request.stdoutMaxBytes || 262144, 262144),
      };
    },
    run(spec) {
      return call(
        "shell.run",
        {
          command: spec.command,
          workdir: spec.workdir,
          timeoutMs: spec.timeoutMs,
          stdoutMaxBytes: spec.stdoutMaxBytes,
        },
        spec.signal,
      );
    },
    start() {
      throw new Error("Use WorkOS application preview for persistent processes");
    },
  });
}
