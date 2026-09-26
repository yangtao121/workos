# LAN Code P0 failure and authorization acceptance

Run this gate after the [resident Code browser gate](lan-code-p0-browser-e2e.md) has left its isolated WorkOS Code workload running. It tests the existing workload; it does not create, stop, restart, save, or delete it. The owner runs it locally because the agent does not receive the owner password.

The normal owner-run [`run-browser-p0.sh`](../../tools/lan/run-browser-p0.sh) should invoke this gate as its final stage, before it removes its temporary password file. For a staged run, keep the same owner-only password fixture and prepared project record used by `test-code-p0.sh`, then run:

```sh
cd /home/aquatao/workos
WORKOS_LAN_E2E_USERNAME=aquatao \
WORKOS_LAN_E2E_PASSWORD_FILE="$credential_file" \
WORKOS_LAN_P0_PROJECT_FILE="$PWD/.workos/lan-code-project-<commit>.json" \
./tools/lan/test-code-p0-failures.sh
```

Both files must be absolute regular paths owned by the current user and mode 0600. Create the password file only by the local steps in [the password browser runbook](lan-password-browser-e2e.md); do not put the password in a command argument, environment variable, chat, or task record. Remove a manually created credential file when finished. The runner imports the local CA into a fresh Chromium NSS store and keeps TLS verification enabled.

The gate uses fresh 1440×900, DPR 1 Chromium profiles and checks four real effects:

1. Open the running Code window, verify nonuniform decoded pixels, use the browser clipboard with a payload larger than 1 MiB, and confirm the visible paste error and absence of that marker from the actual Code buffer. Deny Chromium `clipboard-read` through browser permissions, repeat the paste, verify the visible permission error, restore permissions, and check the marker is absent through Code's native copy action.
2. Start independent public Connect subscriptions to `WatchGreenfieldWindows` and `WatchGreenfieldWindowFrames` using the Code viewer's exact current attachment. Both must deliver a data envelope and remain active before `DetachSurface`; both must end within 15 seconds afterward. The old attachment's clipboard read and input RPCs must return 403, the UI must show the native window service error with no interactive Code window, and the workload must remain running.
3. Sign in two new profiles, open Code on the second, and start two new active media subscriptions. The first profile revokes only that second test device. Both active subscriptions must end within 40 seconds, protected RPCs must return 401, and Code must no longer accept input while the Desktop or auth gate visibly reports unavailable access. The first profile must remain authenticated.
4. If every assertion passes, save only sizes and cutoff timings in an owner-only `.workos/lan-p0-failures.*` directory. Native frame bytes, Code contents, password, cookies and markers are never saved there. Playwright screenshots, traces and video are disabled.

The test uses the live Gateway and Runtime. A denied new request, an HTTP 404, a test fixture canvas, or a mocked clipboard response does not pass a stream-cutoff assertion. A failed gate is evidence of a remaining P0 issue; keep A09 and the overall resident Code capability below `working` until the actual run passes and is recorded in [the task](../tasks/20260926-lan-p0-failure-gate.md).
