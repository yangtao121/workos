# Resident auxiliary applications

The V3 P0 Desktop uses the existing WorkOS Terminal and adds Mousepad as a
lightweight graphical text editor. WorkOS Code and Mousepad are separate
`NativeSession` records with `NativeApplication` persisted by Runtime. A
Code session created by an older client keeps the Code default. Runtime
launches one networkless Greenfield child per Native session, passing the
persisted application kind as a fixed child argument. The child image pins
Mousepad 0.5.10-2. Mousepad starts with an empty document; the user can open
project files through its normal graphical interface when a workspace is
mounted.

Mousepad uses `GDK_BACKEND=x11` because the pinned Greenfield rc1 browser
bridge currently publishes its XWayland window path. The compositor checks
window geometry every 250 ms before its full-frame capture: GTK may map its
first buffer after the display scene's initial refresh, and a static editor
window otherwise remains invisible. The check only publishes changes when
window facts differ. Each child's Unix socket, compositor and application
process remain scoped to that session; a separate Code session is unaffected
by closing or stopping the editor.

The Desktop Home entry asks for `NATIVE_APPLICATION_TEXT_EDITOR` and discovers
running sessions through `ListProjectSurfaces` plus `GetNativeSession`, so a
later browser attach refocuses the correct app instead of creating a second
child. Closing the Desktop parent window detaches its viewer. The Running
apps Stop action ends the workload, and Restart retains its session ID with
a new generation. The Terminal has its own PTY lifecycle and clipboard
controls; its browser clipboard paste is serialized into UTF-8 chunks that
fit Runtime's 16 KiB write limit.

An isolated UID-matched child probe has received a real Mousepad PNG frame,
reconnected with the same native window ID, and applied native focus input.
The owner-run HTTPS gate in `tools/lan/test-p0-aux-apps.sh` checks the live
Code/editor/Terminal identities, Unicode clipboard round trips, editor and
Terminal detach/reopen/Stop isolation, and a Terminal paste larger than
16 KiB. Its result is pending; the probe alone does not establish LAN browser
acceptance. Evidence and remaining work are recorded in
`docs/tasks/20260926-p0-auxiliary-editor.md`.
