# Shared desktop client

The browser renders the owner desktop from Core `DesktopService`. The first
confirmed project walk supplies a one-time initialization candidate: Home,
the verified previous project, and recognized canonical references from the
old device layout. Core revalidates every reference and ignores initialization
once the owner desktop exists. Cached projections are never rendered before
an authenticated `GetDesktop`.

The client applies typed operations rather than uploading a local snapshot.
Watch responses replace the projection, use monotonically increasing revisions,
and persist the revision together with canonical window references in one
origin-local storage value. Explicit reset responses replace the cursor.
Reconnection, foregrounding and online events re-read server authority before
watching again. Offline commands fail immediately; lost responses cause a
refresh, never automatic replay of an old command.

Project, window presence/order/focus and selected conversation are shared.
Desktop positions and sizing remain local. The responsive shell renders the
same focused window on phone/tablet; Home and the primary Agent navigation
participate in shared operations. Agent Sessions is the primary Agent entry;
Tasks and approvals remains accessible from Home and the command palette.

Interactive program creation belongs to explicit launcher actions. The launcher
records the returned workload identity and verified generation before publishing
a window. Restored PTY/Native bodies attach only that workload generation and
never create a replacement. Explicit restart republishes the resulting generation,
remounting attached bodies across devices. Closing a window detaches its device
views; Stop terminates the program. Input ownership still requires explicit
takeover and cannot be acquired by a mirrored focus change.

Installed-app targets contain only installation identity and, for running
programs, exact workload/generation preconditions. Each device obtains its own
surface using `attach_only`; absence or replacement of the program produces an
unavailable view. Device Surface IDs, URLs and bridge credentials remain memory
only and never enter the shared state or projection cache. Surface capability
expiry renews the exact access view, without extending credentials or starting
programs. Explicit new launches and restarts request `MANUAL_STOP`; observers
preserve the existing program policy.

Verification: `sharedDesktop.test.ts`, exact-generation Terminal/Native tests,
window reconciliation tests, and `shared-desktop.spec.ts` exercise the consumer
with a real Connect HTTP stream and three independent browser contexts. The
integrated Core/Runtime gate and physical Android/iPhone/iPad trial remain
separate evidence; Linux WebKit does not establish real-device Safari support.
