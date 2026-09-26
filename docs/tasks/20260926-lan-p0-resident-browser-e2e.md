# Task: LAN P0 resident Code browser gate

- Status: in_progress; implementation and live evidence are separate.
- Owner/branch/worktree: Codex, `feat/lan-p0-resident-browser-e2e`, `/home/aquatao/workos-lan-p0-resident-e2e`.
- Baseline: `338a06b`; the worktree was clean at creation. Fast-forwarded committed Runtime and visible takeover UI through `b0d5c58` before final static checks. Main's simultaneous LAN launcher edits belong to another task.
- Scope: a real, CA-trusted Chromium gate for isolated WorkOS Code in the prepared tracked-source snapshot. No product UI, Runtime, child, Proto or migration changes.
- Dependencies: ADR-0040 resident child/Go broker and UI, LAN password mode, HTTPS CA, and `tools/lan/prepare-code-project.py` project binding. The password fixture is supplied by the owner as an absolute, mode-0600 file.

## Acceptance

- [ ] The actual Code top-level yields nonuniform, decoded canvas pixels at fixed 1440×900, DPR 1; the protected project/workload RPCs identify one running native workload.
- [ ] Through Code GUI, the first profile opens the tracked snapshot's `README.md`, inserts an unsaved marker and verifies it through the real native clipboard. A separate no-network, root-only, read-only helper proves disk SHA-256 remains unchanged without relaxing the child-owned snapshot's 0700 permission.
- [ ] After its native window and every browser profile close, a new profile reopens the same workload/generation/window. The owner-only host runner compares Docker child ID, start instant and host PID before/after. The unsaved marker remains in Code and off disk.
- [ ] A second independent profile watches the same real pixels without input control. Visible Code-window takeover advances the control generation; the old attachment's input fails. The new controller saves through Code GUI, and the isolated helper verifies snapshot bytes contain both markers.
- [ ] At least 30 real keyboard-to-native-Code-canvas pixel samples produce raw timings and nearest-rank p95, with fixed browser/viewport/DPR, RTT, host/container resource and PNG path metadata. If a reliable Code pixel signal cannot be established, report A10 NOT_RUN instead of using an RPC or fixture canvas as latency.
- [ ] Stop moves the real workload to a terminal state; Restart advances its generation, yields a new child identity and real Code pixels. These actions apply only to the isolated test project.
- [ ] A02: Chromium opens Code Quick Open and sees a change in decoded native pixels before selecting `README.md`. This covers one real popup; full menu/dialog parent stacking, focus and close behavior still need separate evidence.
- [ ] A03: a fresh 1440×900 Chromium profile at DPR 2 receives a Code frame with approximately two native pixels per CSS pixel. Resizing the WorkOS window must change the native frame dimensions and retain that ratio and nonuniform pixels.
- [ ] A04: Chromium composition-end text reaches the actual Code buffer exactly once, a held Ctrl key is released when native focus blurs, keyboard shortcuts and mixed clipboard content round-trip, and a wheel event visibly scrolls Code pixels. A dispatched composition event is not a physical OS IME test.
- [ ] No TLS bypass, mock response, credential-bearing screenshot/trace/video, password or cookie in output. No physical Mac or second-device claim from local Chromium profiles.

## Contract and data

Existing `ProjectService`, `SurfaceContinuityService`, `NativeSessionService` and `GreenfieldWindowService` only. The gate writes exclusively inside the prepared snapshot through the Code GUI. It creates temporary Gateway login sessions and one native workload for that isolated project; it never stops any preexisting workload. The runner fails if the project already has a running native workload.

## Verification and handoff

Implemented [runner](../../tools/lan/test-code-p0.sh), [four-phase browser spec](../../apps/desktop-web/e2e/lan-code-p0.spec.ts), [read-only snapshot helper](../../tools/lan/check-code-p0-snapshot.mjs) and [runbook](../runbooks/lan-code-p0-browser-e2e.md). The runner requires the existing owner-only raw UTF-8 password fixture and explicit mode-0600 prepared project record as absolute paths; it does not create or change the owner password. One trailing newline in the password file is accepted. No screenshot is saved because this test does not change product UI and credential-bearing browser captures are prohibited. The viewer task owns deterministic before/after/current visual evidence.

Static verification in the Node 24 container, using existing pinned dependencies from the main checkout:

- `node node_modules/typescript/bin/tsc --noEmit` in `apps/desktop-web`: pass.
- `node node_modules/eslint/bin/eslint.js apps/desktop-web/e2e/lan-code-p0.spec.ts`: pass.
- `node node_modules/prettier/bin/prettier.cjs --write` on the spec and two Markdown files: pass.
- `node node_modules/@playwright/test/cli.js test lan-code-p0.spec.ts --list`: one expected case discovered.
- `bash -n tools/lan/test-code-p0.sh`, Node 24 `node --check tools/lan/check-code-p0-snapshot.mjs`, and `git diff --check`: pass.
- Synthetic, no-network, root-read-only snapshot helper: `hash` returned only a SHA-256 digest; `saved` accepted exact generated markers and rejected an appended stale-controller marker with exit 1. No repository content was printed.
- Runner with both required file variables unset: exit 2 and the expected private-input error, before network or Docker work.
- A02/A03/A04 increment: Node 24 `tsc --noEmit` in `apps/desktop-web`, ESLint on the changed spec, Prettier, Playwright `--list` (one case), and `git diff --check` pass. These are static/discovery checks, not live Code acceptance. `dpr2.json` will hold only frame/CSS dimensions and viewport after a successful owner run.

The owner password was set locally after the LAN launcher repair. The owner chose to run the browser acceptance themselves and will not share a password fixture path. This branch only has static checks: no credential-bearing live run or A01–A10 result is claimed. The owner runs [`run-browser-p0.sh`](../../tools/lan/run-browser-p0.sh) on the final main checkout and records each phase's actual result here. The A02 Quick Open assertion covers one popup, not the entire menu/dialog acceptance; A03 checks native frame scale and refresh after resize, not subjective text clarity; A04 composition is a Chromium DOM event delivered to real Code, not a physical OS IME. A physical second device remains untested by two isolated local profiles. Standard Chromium is the P0 browser target; a physical Mac is not a P0 gate.
