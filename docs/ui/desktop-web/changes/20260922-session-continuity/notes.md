# Session continuity visual evidence

Task: [session continuity](../../../../tasks/20260922-session-continuity.md).

Six states: the selected session with completed input/summary and a local draft,
then the same session immediately after pressing Send while offline, each at
1440×900, 820×1180, and 390×844. Viewport scale factor is 1, route `/`, fixed UTC
clock `2026-09-06T09:00:00Z`. Studio, Studio source, provider `fixture`, all messages
and IDs are deterministic test data. No actual account, credentials, provider or
external service is used.

Before renders baseline commit `b4f7020`; after renders the real session component
from `39daa2e` on the shared desktop. The same HTTP fixture and browser actions are
used. Existing `current/` images did not describe these states, so the before
images were captured from an isolated baseline worktree instead of substituting
an unrelated old screenshot.

Intentional difference: the old offline Send clears the composer and shows a
failed pending input. The new component keeps the draft, displays its real offline
notice, and makes no SubmitSessionInput request. No DOM injection, custom overlay
or screenshot text modification is performed. The completed message and bounded
server summary remain visible in both versions.

The test uses `shared-desktop-fixture.ts` with an actual Connect HTTP stream for
the selected desktop window, and deterministic session/workspace RPC fixtures.
Production UI components render every pixel. Client snapshots and offline behavior
are exercised by normal browser controls; no screenshot-only product route was
added.

Capture commands against separate Vite servers for baseline and after:

```sh
WORKOS_E2E_URL=http://127.0.0.1:5186 \
WORKOS_SESSION_BASELINE=true \
WORKOS_SESSION_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260922-session-continuity/before \
pnpm exec playwright test session-continuity-visual.spec.ts --workers=1

WORKOS_E2E_URL=http://127.0.0.1:5185 \
WORKOS_SESSION_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260922-session-continuity/after \
pnpm exec playwright test session-continuity-visual.spec.ts --workers=1
```

Runner: `workos-playwright-shared:1.62.1`, Chromium. Browser subdirectories prevent
parallel projects from overwriting outputs. The six Chromium `after/` images also
update the same named `current/` PNGs. Screenshots were inspected at mobile and
desktop sizes. All images are below 2 MiB.

The same tests also pass with `--browser webkit`, storing disposable outputs under
`tmp/`; those results are browser-engine behavior evidence. Physical Android,
iPhone and iPad acceptance remains separate. Later journal race fixes may require
recapture on the final integration tree, using the same fixture and commands.
