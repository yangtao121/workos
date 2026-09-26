# LAN P0 Code workspace

Use this procedure for the current WorkOS checkout selected for P0 Code acceptance. It creates a writable copy of tracked files at the final integration commit. The live checkout, `.git`, and `.workos/lan-tls/ca.key` are never mounted into the untrusted Code child.

1. Start the LAN stack, then set the owner password locally with `./tools/lan/start.sh set-password`. The prompt accepts a 1–80 character username and a 12–1024 byte UTF-8 password. It is interactive; do not put the password in shell arguments, environment variables, chat, or task records.
2. Create a mode-0600 temporary password file as described in [the browser gate](lan-password-browser-e2e.md). Set `WORKOS_LAN_E2E_USERNAME` if the owner name differs from `owner`.
3. After the final integration commit, run:

   ```sh
   ./tools/lan/prepare-code-project.py prepare --password-file "$credential_file"
   ./tools/lan/start.sh up
   ./tools/lan/prepare-code-project.py bind --password-file "$credential_file"
   rm -f "$credential_file"
   ```

`prepare` logs in through the CA-verified public Gateway, creates an idempotent P0 project, exports the tracked snapshot, and writes only the owner/project IDs and snapshot path to owner-only local files in `.workos`. `start.sh up` reads the local mount record when no explicit `WORKOS_RUNTIME_WORKSPACE_MOUNTS` is set. `bind` checks that Runtime reports the exact snapshot before binding it through Core's public workspace API. Both commands log out the setup browser session. They refuse a missing password fixture or a mismatched snapshot; they never create a credential-bearing test screenshot.

Open the reported project in the HTTPS Desktop, then start Native/WorkOS Code. Editing the snapshot cannot change the live checkout. The P0 result must separately record actual Code pixels, modified file bytes, workload/container identity across reconnect, observer/control handoff, input and clipboard behavior, and measured latency. The scripted Chromium result does not claim physical macOS or second-device evidence.
