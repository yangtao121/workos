# Greenfield browser input adapter (P0)

`apps/desktop-web/src/greenfieldCompositor.ts` binds the pinned
`@gfld/compositor@1.0.0-rc1` renderer directly to the WorkOS canvas. The
package's `initScene` also installs automatic clipboard reads on focus and
unreported clipboard writes, so WorkOS binds the scene, pointer, wheel, and
keyboard events itself. Runtime still owns the Code process, display session,
and server-side control policy. Browser event gating is a user interface
measure, not an authorization boundary.

The focused hidden textarea receives Chromium composition events. A committed
string becomes a bounded UTF-8 Wayland text offer followed by Control+V. Mac
Meta editing shortcuts map to Linux Control; physical Control remains physical,
so terminal Control+C can still interrupt a process. Focus loss releases held
keys and pointer buttons. Pointer coordinates map CSS letterboxing to the
canvas backing pixels.

Copy requires a fresh application selection after Control+C. The browser
reads `text/plain;charset=utf-8` or `text/plain` through the Greenfield
data source and writes it to `navigator.clipboard` only from a user action.
Paste reads the browser clipboard from a user action, offers the text to the
application, and sends Control+V. Both directions reject text above the
`clipboardMaxBytes` value returned by Runtime's `OpenGreenfieldDisplay` RPC
(currently 256 KiB). The planned resident viewer contract raises this to 1 MiB.
The UI reports browser permission errors and missing application selections;
the paste status states that an instruction was sent because the protocol has
no application-consumption acknowledgement.

The pinned Greenfield proxy still has one signaling client and no replay of
the Wayland window graph after reconnect. The P0 browser adapter cannot
establish multi-device observation or same-instance visual continuation
until Runtime implements that boundary. The deterministic Chromium tests
exercise browser events and a local fake seat; real Code, macOS IME, and
application clipboard exchanges remain live acceptance cases in
[`20260924-v3-p0-native-experience.md`](../tasks/20260924-v3-p0-native-experience.md).
