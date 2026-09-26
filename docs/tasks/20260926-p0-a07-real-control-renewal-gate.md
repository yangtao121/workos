# Task: P0 A07 real Code device identity and control renewal gate

- Status: in_progress
- Branch/worktree: `feat/p0-a07-real-control-renew` / `/home/aquatao/workos-p0-a07-renew`, based on `main@553007c`.
- Scope: strengthen the existing real Code continuity phase with distinct Gateway device IDs, and add a separate final owner-run Chromium stage that waits for actual automatic control renewal before typing into Code. No Runtime, Proto, migration or product UI change.
- Dependencies: V3 P0 A07, `SurfaceContinuityService.GetSurfaceControl`, the Desktop `NativeSessionLease` timer and the CA-trusted Code browser gate. The A08 lifecycle task changes the Code runner independently; this task does not edit its lifecycle phases.

## Acceptance

1. The two independent Chromium contexts in the Code continuity phase report different authenticated Gateway device IDs; the second context's control lease belongs to its own device.
2. After earlier Code, A08 and authorization stages, a separate browser process attaches to the unique running Code workload of the exact prepared project without launching a new program. The server's `controlExpiresAt` advances while device ID, attachment ID, control generation, workload generation and native window ID remain unchanged.
3. The same controller types a fresh marker into real Code after that advancement and reads it back exactly once through the browser Clipboard API. Closing the viewer leaves the workload running.
4. The long gate uses the real 30-minute Runtime lease and the actual Desktop renewal timer. It polls the server lease fact instead of sleeping a guessed duration, and records bounded nonsecret identity/timing metadata. No local credential is consumed by static checks.

## Verification and handoff

- The [Code continuity spec](../../apps/desktop-web/e2e/lan-code-p0.spec.ts) now reads `GetCurrentDevice` in both authenticated Chromium contexts, requires distinct device IDs and verifies that the second device owns the post-takeover lease.
- The separate [real renewal spec](../../apps/desktop-web/e2e/lan-code-control-renewal.spec.ts) locates Code through the Native session's `application` field, so a simultaneous graphical editor workload cannot be mistaken for Code. It polls the server's actual control expiry for up to 35 minutes, fails immediately on controller identity drift, then types and copies a marker from the same Code window. It prints only elapsed minutes and server lease UTC expiry at start, every five minutes and on renewal. The [runner](../../tools/lan/test-code-control-renewal.sh) uses only the existing exact-commit project record and owner-only password fixture. The [one-command wrapper](../../tools/lan/run-browser-p0.sh) invokes it as the final stage, leaving earlier results available if this long stage fails. Integration must place auxiliary and A08 stages before this final stage.
- Static verification: Desktop Web TypeScript `tsc --noEmit`, targeted ESLint, Prettier, `bash -n` on the new runner, `sh -n` on the wrapper, `git diff --check`, and Playwright test discovery passed in pinned Node/Chromium containers. The runner's no-password/no-project negative path exited 2 before Docker or network work.
- The owner-authenticated LAN run has **not** executed; the estimated 30-minute wait is deliberate because Runtime's real control TTL is 30 minutes and Desktop renews shortly before expiry. The result is not a P0 A07 PASS until that owner run succeeds. No UI changed, so no new visual capture is required.
- A03 text clarity is still limited by the existing real Code gate's DPR geometry and nonflat-pixel checks. A bounded arbitrary pixel-contrast threshold would not prove legible text or distinguish native glyph detail from unrelated UI, so this task does not add one.
