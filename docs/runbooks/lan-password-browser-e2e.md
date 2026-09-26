# LAN HTTPS password browser gate

This gate exercises the real Gateway and Desktop in two isolated Chromium browser contexts. It checks TLS trust, secure-context browser APIs, password sign-in, cookie attributes, protected Core and Runtime paths, device revocation, and logout. It does not replace the physical Mac Chrome/Edge and second-device P0 acceptance.

## Preconditions

1. The LAN stack is running at `https://<WORKOS_LAN_IP>:8443` through `./tools/lan/start.sh` with Gateway password mode. The host's `WORKOS_LAN_TLS_DIR/ca.crt` is the CA that signed the active leaf certificate.
2. Set the single-owner username and password through the private `./tools/lan/start.sh set-password` command. Use a disposable test deployment; password changes revoke existing sessions.
3. Install workspace dependencies (`pnpm install --frozen-lockfile`) if `apps/desktop-web/node_modules` is missing. The test script builds the pinned Playwright image with `certutil` if needed.
4. Create an owner-only password fixture. Do not place it in the repository or print the password:

   ```sh
   umask 077
   credential_file=$(mktemp)
   printf 'LAN test password: ' >&2
   stty -echo
   IFS= read -r credential
   stty echo
   printf '\n' >&2
   printf '%s' "$credential" > "$credential_file"
   unset credential
   ```

## Run

```sh
WORKOS_LAN_IP=192.168.5.5 \
WORKOS_LAN_E2E_USERNAME=owner \
WORKOS_LAN_E2E_PASSWORD_FILE="$credential_file" \
./tools/lan/test-browser.sh
rm -f "$credential_file"
```

The runner imports `ca.crt` into a fresh NSS profile and launches Chromium with certificate verification enabled. It never uses `ignoreHTTPSErrors` or `--ignore-certificate-errors`. Playwright traces, screenshots, and video are disabled for this credential-bearing test. The test makes no project writes and revokes only its newly signed-in devices.

For the real expiry phase, configure the Gateway with `WORKOS_AUTH_SESSION_TTL=5m`, restart it, and add `WORKOS_LAN_PASSWORD_EXPIRY_E2E=true` to the invocation above. That phase waits for actual server expiry, re-adds the stale cookie with a future browser expiry, and expects the Gateway to reject it. The regular phase takes under three minutes; the expiry phase adds about five minutes.

Record the command, commit, server configuration, browser result, and any failure in [the task record](../tasks/20260926-lan-browser-e2e.md). Never record the password, cookie value, bearer token, full user data, Playwright trace, or a credential-bearing screenshot.
