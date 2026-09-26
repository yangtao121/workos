#!/usr/bin/env node
// Run only in an isolated, no-network, root read-only helper container. The
// browser and host owner cannot traverse the child-owned 0700 snapshot. Print
// hashes/verdicts only, never repository contents.
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";

const content = readFileSync("/snapshot/README.md");
const hash = createHash("sha256").update(content).digest("hex");

if (process.argv[2] === "hash") {
  process.stdout.write(`${hash}\n`);
} else if (process.argv[2] === "saved") {
  const state = JSON.parse(readFileSync("/results/state.json", "utf8"));
  const saved = JSON.parse(readFileSync("/results/saved.json", "utf8"));
  const text = content.toString("utf8");
  if (
    !/^[0-9a-f]{64}$/.test(state.originalSha256) ||
    !/^WORKOS_P0_UNSAVED_[0-9a-f-]{36}$/.test(state.unsavedMarker) ||
    !/^WORKOS_P0_DENIED_[0-9a-f-]{36}$/.test(saved.deniedMarker) ||
    typeof saved.payload !== "string" ||
    !saved.payload.includes("WORKOS_P0_SAVED_") ||
    hash === state.originalSha256 ||
    !text.includes(state.unsavedMarker) ||
    !text.includes(saved.payload) ||
    text.includes(saved.deniedMarker)
  ) {
    process.stderr.write("test-code-p0: isolated snapshot save bytes did not match Code input\n");
    process.exitCode = 1;
  } else {
    process.stdout.write("isolated snapshot save bytes verified\n");
  }
} else {
  process.stderr.write("test-code-p0: invalid snapshot check mode\n");
  process.exitCode = 2;
}
