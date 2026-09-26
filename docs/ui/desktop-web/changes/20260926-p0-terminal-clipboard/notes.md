# Terminal clipboard visual record

- Task: [20260926-p0-terminal-clipboard.md](../../../../tasks/20260926-p0-terminal-clipboard.md)
- Surface/state: Terminal window with controlling session and fixed UTF-8 output. `before/` shows the previous toolbar; `after/` adds **Copy selection** and **Paste**. Status text is exercised by the browser and component tests.
- Route: `/`, initial shared Desktop window `{ kind: "terminal", projectId, workloadId }`.
- Fixture: `apps/desktop-web/e2e/terminal-clipboard.spec.ts` uses fixed `Studio` project, terminal/attachment IDs, `fixture@workos` shell text, and a deterministic Connect responder. No real project content, credentials, provider, or external service.
- Browser: Playwright Chromium 1.62.1 image; viewport 1440×900; device scale factor 1; animation disabled. Both images were captured with the same route, fixture, viewport and terminal state, before and after the UI change.
- Capture command: `WORKOS_E2E_URL=http://127.0.0.1:5198 WORKOS_TERMINAL_CAPTURE_DIR=<task>/before|after node node_modules/@playwright/test/cli.js test terminal-clipboard.spec.ts --grep "captures the fixed" --workers=1` inside `workos-playwright:1.62.1` against the local Vite fixture server.
- `current/terminal-window--clipboard-controls--1440x900.png` matches `after/`.
