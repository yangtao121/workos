# LAN resident Code browser acceptance

This gate uses real, CA-trusted Chromium profiles against the HTTPS Gateway and the prepared, isolated WorkOS source snapshot. It appends test text to that snapshot's `README.md` through the actual Code GUI. It creates and later stops/restarts only this project's new native workload. Do not point it at a project that already has a running native workload.

## Preconditions

1. Finish the [LAN Code workspace preparation](lan-code-p0.md): owner password set locally, tracked snapshot prepared, resident stack running, and project workspace bound. The browser gate does not create the project or set a password.
2. Install the pinned workspace dependencies (`pnpm install --frozen-lockfile`) if `apps/desktop-web/node_modules` is absent. Docker and the pinned Playwright image are required.
3. Create the owner-only temporary password fixture from [the password browser gate](lan-password-browser-e2e.md). Keep its contents out of chat and shell arguments.
4. Pass the absolute path to the mode-0600 `.workos/lan-code-project-<commit>.json` record made by `prepare-code-project.py prepare`. The runner checks that the record names an absolute snapshot without `.git` or `.workos`; a separate no-network, read-only helper verifies its `README.md` bytes without loosening the child-owned directory permissions.

## Run

```sh
cd /home/aquatao/workos
WORKOS_LAN_E2E_USERNAME=aquatao \
WORKOS_LAN_E2E_PASSWORD_FILE="$credential_file" \
WORKOS_LAN_P0_PROJECT_FILE="$PWD/.workos/lan-code-project-<commit>.json" \
./tools/lan/test-code-p0.sh
rm -f "$credential_file"
```

The runner imports `ca.crt` into Chromium's fresh NSS profile. Certificate verification remains enabled; the test has no route mocks. Passwords and cookies are absent from the results. Playwright screenshots, traces and video are disabled because the browser contains credentials and the source snapshot can contain user content.
Use the exact username entered in `set-password` if it differs from the example above.

The four phases have separate browser processes:

1. `unsaved`: launch Code, decode real pixels, edit `README.md`, copy the unsaved text from Code, then close every viewer. The isolated helper proves disk SHA-256 unchanged after the browser exits.
2. `continuity`: a new browser reopens the exact workload/generation/window and copies the unsaved buffer. A second profile observes the same pixels, takes control through the visible Code window action, and the old control epoch fails an input attempt. The controller pastes more than 4096 mixed text characters and saves; the isolated helper verifies snapshot bytes while the browser checks the native copy.
3. `restart`: use the isolated project's Running apps Stop and Restart controls, then confirm a new generation and Code window.
4. `latency`: type 30 four-key groups in the real Code editor. Each sample starts at Chromium's actual `keydown` and ends when at least 25 sampled pixels change on the resident Code canvas. A native copy checks that all typed groups reached Code. Raw samples, nearest-rank p95, five Gateway round-trip probes, browser version, viewport/DPR and PNG path are saved. The initial target is p95 ≤100 ms; an absent or ambiguous Code signal is `NOT_RUN`, not a passing proxy measurement.

Between phases 1 and 2, the host records the resident Docker child's ID, start instant and host PID. All three must remain equal after the all-client disconnect. After Stop/Restart, the new child must differ. The runner leaves the restarted isolated workload running for inspection and stores only nonsecret facts under the printed `.workos/lan-p0-e2e.*` directory, including Docker resource samples and host CPU/GPU facts when available.

This Linux Chromium gate does not constitute real macOS Chrome/Edge or a second physical device. A01/A05/A06/A07/A08/A10 evidence must be recorded with the actual command/result in [the task](../tasks/20260926-lan-p0-resident-browser-e2e.md). If an earlier phase fails, later phases are not claimed as run. A03 DPR 2 and physical Mac tests remain separate.
