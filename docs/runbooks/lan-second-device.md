# Runbook: second LAN device continuation and takeover (A16)

This is the operator runbook for acceptance row **A16** of
[the V2 continuity task](../tasks/20260915-v2-agent-workspace-web-continuity.md).
It stays a **BLOCKED** row until an operator executes it on real hardware and
records the evidence below. Nothing in this runbook may be satisfied by two
tabs, two browser contexts, two viewports, or two containers on one machine —
those prove software control logic, not the physical LAN chain.

## What this run proves

A second physical device on the same LAN, paired through the existing device
pairing flow, discovers the owner's project, finds a continuously running
application (PTY/native workload) through **Running apps** (server-side
facts, not device-local storage), takes control explicitly, and the first
device's inputs are then rejected server-side.

## Preconditions

1. The WorkOS stack runs on the LAN host with TLS + production device auth
   (`make test-lan-pairing` passes on the same host; the gateway profile
   `workos-gateway-tls` and `workos-mdns-announce` are the reference wiring).
2. `WORKOS_RUNTIME_NATIVE_CANDIDATES=lan` is set on runtime-host for native
   media (host networking required; loopback is the fail-safe default). For a
   PTY workload this is not needed.
3. Device A (existing paired device, desktop browser) is logged in and has a
   project open with a running continuous workload (a Terminal session with
   identifiable content is sufficient; a native session additionally proves
   the media path).
4. Device B is a distinct physical machine (or a physically separate
   browser+OS account on spare hardware that has never held A's profile)
   on the same LAN segment, with a Chromium-based browser.

## Steps

1. **Pair device B.** On the LAN host, rotate a pairing ticket:
   `docker compose --profile lan-pairing exec -T workos-gateway-tls /usr/local/bin/workosctl device pair`
   (or use the mDNS discovery flow: `workosctl device scan --fingerprint <sha256-of-leaf> --timeout 6s`,
   then `device pair`). Open the printed `https://<lan-host>:8443/#pair=...`
   URL on device B and complete the pairing proof. Name the device
   distinctly (e.g. `LAN Acceptance B`).
2. **Open the project.** In device B's browser, enter the WorkOS desktop,
   open the same project A is using. Verify the file view and Terminal see
   the same workspace content A sees.
3. **Find the running app.** Open **Home → Running apps**. The workload A
   started must appear with its server-side state (`running`), attachment
   count, and display name.
4. **Record pre-takeover facts** (see Evidence below): workload id and
   generation, controller device id and control generation.
5. **Open and take control.** Click **Open** on the running app row (this
   attaches the SAME workload instance — no second process is created). The
   window opens in observer mode with **Take control** available. Click
   **Take control** (RequestSurfaceControl). The control generation must
   advance (typically +1) and device B becomes the controller.
6. **Prove B's input works.** Type into the terminal (or drive the native
   window) and observe the program's content change.
7. **Prove A's input is rejected.** On device A, without refreshing, type
   into the same session's window: the write must be refused
   (`PermissionDenied` surfaced as the input-disabled / not-controller
   state). A's queued input, resize, and late renewals must not affect the
   program. A may re-attach (observer) but must not regain control without
   its own explicit takeover.
8. **Detach vs stop.** Close B's window (detach only): the program keeps
   running (Running apps still lists it). Re-open from Running apps and
   re-take control. Finally use the explicit **Stop** control: the workload
   leaves Running apps and its attachments expire.

## Evidence to record (append to the task record's A16 row)

- Topology: host + two devices, LAN segment, browser versions, origin
  (`https://<host>:8443`), `WORKOS_RUNTIME_NATIVE_CANDIDATES` value.
- Device ids of A and B (Device Center) and their names.
- Workload id + generation before B's takeover, and the control generation
  before/after (from Device Center / the takeover UI state or gateway logs).
- A screenshot or sanitized log excerpt of A's rejected input attempt after
  B's takeover, and of B's successful input.
- For native workloads: confirmation that B actually rendered the video and
  its input changed the application (not a frozen frame).

## Out of scope (do not record as passed here)

Public-internet traversal, TURN/STUN, simultaneous multi-controller input,
third-device arbitration, and mobile-native chains (P4).
