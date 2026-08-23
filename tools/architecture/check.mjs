import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";

const repositoryRoot = process.cwd();
const roots = ["sdk", "clients", "apps"];
const packages = new Map();
const problems = [];

for (const root of roots) {
  for (const entry of await readdir(path.join(repositoryRoot, root), { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const directory = path.join(repositoryRoot, root, entry.name);
    let manifestText;
    try {
      manifestText = await readFile(path.join(directory, "package.json"), "utf8");
    } catch (error) {
      if (error.code === "ENOENT") continue;
      throw error;
    }
    const manifest = JSON.parse(manifestText);
    packages.set(manifest.name, { directory, layer: root, manifest });
  }
}

for (const [name, value] of packages) {
  const localDependencies = Object.entries({
    ...value.manifest.dependencies,
    ...value.manifest.peerDependencies,
    ...value.manifest.devDependencies,
  }).filter(([dependency]) => packages.has(dependency));

  for (const [dependency, version] of localDependencies) {
    const target = packages.get(dependency);
    if (version !== "workspace:*") {
      problems.push(`${name}: local dependency ${dependency} must use workspace:*`);
    }
    if (layerRank(value.layer) < layerRank(target.layer)) {
      problems.push(
        `${name}: ${value.layer} cannot depend on higher layer ${dependency} (${target.layer})`,
      );
    }
  }

  if ((value.layer === "sdk" || value.layer === "clients") && !value.manifest.exports) {
    problems.push(`${name}: shared packages require an explicit exports map`);
  }
  if (value.manifest.private !== true || value.manifest.license !== "Apache-2.0") {
    problems.push(`${name}: workspace packages must be private and Apache-2.0 during foundation`);
  }

  const sourceRoot = path.join(value.directory, "src");
  for (const source of await sourceFiles(sourceRoot)) {
    const text = await readFile(source, "utf8");
    for (const specifier of imports(text)) {
      const localName = specifier.match(/^(@workos\/[^/]+)/)?.[1];
      if (
        localName &&
        localName !== name &&
        !localDependencies.some(([dependency]) => dependency === localName)
      ) {
        problems.push(`${relative(source)}: import ${localName} is not declared by ${name}`);
      }
      if (specifier.startsWith(".")) {
        const resolved = path.resolve(path.dirname(source), specifier);
        if (!inside(value.directory, resolved)) {
          problems.push(
            `${relative(source)}: relative import escapes package boundary (${specifier})`,
          );
        }
      }
    }
  }
}

const graph = new Map(
  [...packages].map(([name, value]) => [
    name,
    Object.keys({ ...value.manifest.dependencies, ...value.manifest.peerDependencies }).filter(
      (dependency) => packages.has(dependency),
    ),
  ]),
);
for (const cycle of findCycles(graph))
  problems.push(`workspace dependency cycle: ${cycle.join(" -> ")}`);

if (problems.length > 0) {
  for (const problem of problems) console.error(problem);
  process.exitCode = 1;
} else {
  console.log(`Architecture check passed for ${packages.size} TypeScript workspaces.`);
}

function layerRank(layer) {
  return { sdk: 0, clients: 1, apps: 2 }[layer];
}

async function sourceFiles(directory) {
  const result = [];
  let entries;
  try {
    entries = await readdir(directory, { withFileTypes: true });
  } catch (error) {
    if (error.code === "ENOENT") return result;
    throw error;
  }
  for (const entry of entries) {
    if (entry.name === "gen") continue;
    const value = path.join(directory, entry.name);
    if (entry.isDirectory()) result.push(...(await sourceFiles(value)));
    else if (/\.(?:ts|tsx)$/.test(entry.name)) result.push(value);
  }
  return result;
}

function imports(text) {
  const result = [];
  const pattern = /(?:from\s+|import\s*\()(["'])([^"']+)\1/g;
  for (const match of text.matchAll(pattern)) result.push(match[2]);
  return result;
}

function inside(parent, child) {
  const relation = path.relative(parent, child);
  return relation !== ".." && !relation.startsWith(`..${path.sep}`) && !path.isAbsolute(relation);
}

function relative(value) {
  return path.relative(repositoryRoot, value).split(path.sep).join("/");
}

function findCycles(graph) {
  const cycles = [];
  const complete = new Set();
  const visit = (node, stack) => {
    const index = stack.indexOf(node);
    if (index >= 0) {
      cycles.push([...stack.slice(index), node]);
      return;
    }
    if (complete.has(node)) return;
    for (const next of graph.get(node) ?? []) visit(next, [...stack, node]);
    complete.add(node);
  };
  for (const node of graph.keys()) visit(node, []);
  return cycles;
}
