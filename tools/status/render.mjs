import { readFile, writeFile } from "node:fs/promises";

const status = JSON.parse(await readFile("docs/status.json", "utf8"));
const allowedStates = new Set(["contract-only", "scaffolded", "working", "verified"]);
if (!/^\d{4}-\d{2}-\d{2}$/.test(status.updated) || !Array.isArray(status.modules)) {
  throw new Error("docs/status.json has an invalid date or modules list");
}
const names = new Set();
for (const item of status.modules) {
  if (
    ![item.name, item.process, item.evidence].every(
      (value) => typeof value === "string" && value.length > 0,
    )
  ) {
    throw new Error("every status module requires name, process, and evidence");
  }
  if (!allowedStates.has(item.status)) throw new Error(`invalid status for ${item.name}`);
  if (names.has(item.name)) throw new Error(`duplicate status module ${item.name}`);
  names.add(item.name);
}
const readme = await readFile("README.md", "utf8");
const start = "<!-- status:start -->";
const end = "<!-- status:end -->";

if (!readme.includes(start) || !readme.includes(end)) {
  throw new Error("README status markers are missing");
}

const rows = status.modules
  .map((item) => `| ${item.name} | ${item.process} | \`${item.status}\` | ${item.evidence} |`)
  .join("\n");
const table = `${start}\n\n最后更新：${status.updated}\n\n<!-- prettier-ignore -->\n| 模块 | 进程 | 状态 | 证据 |\n| --- | --- | --- | --- |\n${rows}\n\n${end}`;
const next = `${readme.slice(0, readme.indexOf(start))}${table}${readme.slice(readme.indexOf(end) + end.length)}`;

if (process.argv.includes("--check")) {
  if (next !== readme) {
    throw new Error("README status block is stale; run make docs");
  }
} else {
  await writeFile("README.md", next);
}
