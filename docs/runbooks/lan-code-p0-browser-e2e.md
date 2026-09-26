# LAN resident Code browser acceptance

This gate uses real, CA-trusted Chromium profiles against the HTTPS Gateway and the prepared, isolated WorkOS source snapshot. It appends test text to that snapshot's `README.md` through the actual Code GUI. It creates and later stops/restarts only this project's new native workload. Do not point it at a project that already has a running native workload.

## Owner-run one-command gate

After setting the owner password locally with `./tools/lan/start.sh set-password`, run this from a local interactive terminal on a clean, committed checkout:

```sh
cd /home/aquatao/workos
./tools/lan/run-browser-p0.sh
```

The default username is `aquatao`; set `WORKOS_LAN_E2E_USERNAME` in the environment if the configured owner name differs. Enter the existing password once at the no-echo TTY prompt. Do not put the password in a shell argument, environment variable, chat, or task record. The script keeps its temporary mode-0600 password file outside the checkout and removes it on exit or a catchable interruption. It preserves a mode-0700 `.workos/lan-browser-p0.*` directory containing only commit and stage results. The resident Code gate writes separate nonsecret identity/performance evidence under `.workos/lan-p0-e2e.*` once it starts.

The script bootstraps an unavailable HTTPS Gateway without a password, then prepares the current HEAD's tracked-source snapshot and project, restarts the resident stack with that mount, binds the workspace, and runs `test-browser.sh`, `test-code-p0.sh`, the [active authorization failure gate](lan-code-p0-failure-e2e.md), and the final real Code control-renewal gate in order. The final stage waits for the actual 30-minute Runtime control lease to renew and can take roughly 30 minutes; it fails after a bounded 35-minute renewal wait. It stops at the first failed stage and reports that stage's exit code. Install pinned workspace dependencies first if `apps/desktop-web/node_modules` is absent. This command does not create or change the owner password. The Code test edits and restarts only the isolated project it prepares; the live checkout is never the Code mount.

The manual steps below remain available for a staged investigation or rerun of one browser gate with an existing private fixture. The owner-run command requires no fixture path and is the default acceptance path.

## Preconditions

1. Finish the [LAN Code workspace preparation](lan-code-p0.md): owner password set locally, tracked snapshot prepared, resident stack running, and project workspace bound. The browser gate does not create the project or set a password.
2. Install the pinned workspace dependencies (`pnpm install --frozen-lockfile`) if `apps/desktop-web/node_modules` is absent. Docker and the pinned Playwright image are required.
3. Create the owner-only temporary password fixture from [the password browser gate](lan-password-browser-e2e.md). Keep its contents out of chat and shell arguments.
4. Pass the absolute path to the mode-0600 `.workos/lan-code-project-<commit>.json` record made by `prepare-code-project.py prepare`. The runner checks that the record names an absolute snapshot without `.git` or `.workos`; a separate no-network, read-only helper verifies its `README.md` bytes without loosening the child-owned directory permissions.

## Run

```sh
cd /home/aquatao/workos
export WORKOS_LAN_E2E_USERNAME=aquatao
export WORKOS_LAN_E2E_PASSWORD_FILE="$credential_file"
export WORKOS_LAN_P0_PROJECT_FILE="$PWD/.workos/lan-code-project-<commit>.json"
./tools/lan/test-code-p0.sh
./tools/lan/test-code-p0-failures.sh
./tools/lan/test-code-control-renewal.sh
rm -f "$credential_file"
unset WORKOS_LAN_E2E_PASSWORD_FILE
```

The runner imports `ca.crt` into Chromium's fresh NSS profile. Certificate verification remains enabled; the test has no route mocks. Passwords and cookies are absent from the results. Playwright screenshots, traces and video are disabled because the browser contains credentials and the source snapshot can contain user content.
Use the exact username entered in `set-password` if it differs from the example above.

The five phases have separate browser processes:

1. `unsaved`: launch Code, decode real pixels, open Quick Open and confirm that the actual Code canvas changes before choosing `README.md`, edit the file, copy the unsaved text from Code, then close every viewer. The isolated helper proves disk SHA-256 unchanged after the browser exits.
2. `dialog-close`: reopen the same Code workload, open its real native `Open File` child, and verify the exact parent/window IDs. Focusing Code must leave the transient above its parent. Drag the child's WorkOS title bar and resize it; verify that its position persists, the new native frame fills the resized stage at the active DPR, and the child stays above Code. Closing the child must leave Code and its resident Docker process running.
3. `continuity`: a new browser reopens the exact workload/generation/window and copies the unsaved buffer. A second Chromium context has a different authenticated Gateway device ID, observes the same pixels, takes control through the visible Code window action, and the old control epoch fails an input attempt. The controller commits a Chromium composition event into the real Code buffer once, checks that Ctrl released on blur, pastes more than 4096 mixed text characters, saves, and scrolls until real Code pixels change. The isolated helper verifies snapshot bytes while the browser checks the native copy. After both profiles close, a DPR 2 Chromium profile opens the same window, checks that native frame dimensions are about twice the CSS dimensions, then shrinks the WorkOS window and checks that a new DPR 2 Code frame arrives. The nonsecret dimensions are stored in `dpr2.json`.
4. `restart`: use the isolated project's Running apps Stop and Restart controls, then confirm a new generation and Code window.
5. `latency`: type 30 four-key groups in the real Code editor. Each sample starts at Chromium's actual `keydown` and ends when at least 25 sampled pixels change on the resident Code canvas. A native copy checks that all typed groups reached Code. Raw samples, nearest-rank p95, five Gateway round-trip probes, browser version, viewport/DPR and PNG path are saved. The initial target is p95 ≤100 ms; an absent or ambiguous Code signal is `NOT_RUN`, not a passing proxy measurement.

Between phases 1 and 2, the host records the resident Docker child's ID, start instant and host PID. All three must remain equal after the all-client disconnect. After Stop/Restart, the new child must differ. The runner leaves the restarted isolated workload running for inspection and stores only nonsecret facts under the printed `.workos/lan-p0-e2e.*` directory, including Docker resource samples and host CPU/GPU facts when available.

The final `test-code-control-renewal.sh` stage uses the same owner-only password and exact-commit project record. It identifies Code among any other native workloads through `GetNativeSession.application`, reopens that existing workload, and polls `GetSurfaceControl.controlExpiresAt` until the server reports a later expiry with the same device, attachment, control generation and window identity. It then types into real Code and verifies the marker exactly once through Chromium's text clipboard. It does not start or restart Code. Only nonsecret identity and timing facts are written to `.workos/lan-p0-renew.*/results.json`. This stage is live long-duration evidence; the shorter unit tests for the renewal timer do not substitute for it.

Standard Chromium is the P0 browser target. Record A01–A10 with the actual command/result in [the task](../tasks/20260926-lan-p0-resident-browser-e2e.md); if an earlier phase fails, later phases are not claimed as run. The `Open File` phase tests one real dialog path; it does not exhaust all native menu/dialog stacking patterns. The composition event is dispatched by Chromium into the real Code input path, so it does not establish behavior of a physical OS IME. Two isolated local profiles establish independent browser clients, not a second physical device.
