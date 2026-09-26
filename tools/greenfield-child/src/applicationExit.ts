import { readFile } from "node:fs/promises";

export type ApplicationExit = { exitCode?: number; signal?: string };

// /proc/<pid>/stat keeps the command in parentheses, and it may contain
// spaces or parentheses. The final ')' separates it from fields 3 onward.
export function parseProcessIdentity(stat: string): { state: string; starttime: string } {
  const end = stat.lastIndexOf(")");
  if (end < 2 || stat.at(end + 1) !== " ") throw new Error("APPLICATION_PID_INVALID");
  const fields = stat
    .slice(end + 2)
    .trim()
    .split(/\s+/);
  const state = fields[0];
  const starttime = fields[19]; // field 22, with state at field 3
  if (!state || !starttime || !/^[0-9]+$/.test(starttime))
    throw new Error("APPLICATION_PID_INVALID");
  return { state, starttime };
}

export async function readProcessIdentity(pid: number) {
  if (!Number.isSafeInteger(pid) || pid < 1) throw new Error("APPLICATION_PID_INVALID");
  try {
    return parseProcessIdentity(await readFile(`/proc/${String(pid)}/stat`, "utf8"));
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw error;
  }
}

export function processAlive(
  expectedStarttime: string,
  current: { state: string; starttime: string } | undefined,
): boolean {
  return (
    !!current &&
    current.starttime === expectedStarttime &&
    current.state !== "Z" &&
    current.state !== "X"
  );
}

export function normalApplicationExit(exit: ApplicationExit | undefined): boolean {
  return exit?.exitCode === 0 && exit.signal === undefined;
}
