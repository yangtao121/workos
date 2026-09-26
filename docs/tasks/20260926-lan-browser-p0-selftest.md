# Task: Local LAN P0 browser self-test

- Status: wrapper implemented and statically checked; credential-bearing live browser acceptance is owner-run.
- Owner/branch/worktree: Codex, `feat/lan-browser-p0-selftest`, `/home/aquatao/workos-lan-browser-p0-selftest`.
- Baseline: clean `e2cd58a` main. The owner chose to run the credential-bearing gate locally and will not share a password fixture path.
- Scope: one local-TTY wrapper around the existing CA-verified project preparation, LAN startup, password browser gate and resident Code browser gate. No product UI, Proto, Runtime child or NativeSessionLease changes.
- Dependencies: a Docker-enabled unprivileged host user, the existing owner password, pinned browser dependencies, and the current WorkOS checkout. The wrapper may bootstrap an unavailable LAN Gateway before project preparation, then must restart the resident stack after the snapshot mount is written.

## Acceptance

- [x] Read the password once from `/dev/tty` with echo disabled; never put its bytes in argv, environment, logs, screenshots or the repository. Create an unpredictable mode-0600 temporary file outside the checkout; remove it and restore terminal settings on success, failure and catchable signal.
- [x] Default to username `aquatao` with an explicit environment override. Resolve the owner-only project record for the exact current HEAD and reject a dirty tracked checkout or unsafe record.
- [x] Run prepare → resident `up` → bind → existing password Chromium gate → existing resident Code Chromium gate, stopping at the first failure. Preserve a private, credential-free stage ledger and the Code runner's separate evidence directory.
- [x] Shell syntax, no-TTY/no-secret error path, and static safety checks pass. Live credentials and Code GUI execution remain owner-run.

## Verification and handoff

- [`run-browser-p0.sh`](../../tools/lan/run-browser-p0.sh) is executable and passes `sh -n`, `bash -n`, and `git diff --check`.
- A detached, no-controlling-TTY invocation exits 2 with a bounded error before reading a password; a bad-argument invocation exits 2 with usage. Both leave `/tmp/workos-lan-p0-password.*` unchanged and print no secret. These are the requested no-password negative paths.
- Prettier check passes for this task record and the two updated runbooks. No Proto, product UI or generated files changed, so no UI screenshot or generation run is required. The parent integration task owns `docs/status.json` and repository-wide `make check`.
- The owner runs the command in [the browser runbook](../runbooks/lan-code-p0-browser-e2e.md#owner-run-one-command-gate) from the final committed main checkout. The wrapper's stage ledger records the exact commit and exits at the first failed stage; the existing Code gate stores its separate real-browser evidence. No live acceptance result is claimed here.
