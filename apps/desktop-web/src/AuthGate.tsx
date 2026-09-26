import { Code, ConnectError } from "@connectrpc/connect";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";

import type { AuthDeploymentMode, DeviceAuthClient } from "@workos/device-auth";
import { isUnavailable, parsePairingFragment } from "@workos/device-auth";
import { Button } from "@workos/ui-kit";
import { clearLocalDesktopState } from "./desktopLocalState.js";

// The Auth Gate state machine gates the entire Desktop: no business request
// is issued before a device session exists.
//
//	checking-session      — asking the Gateway whether the cookie is live
//	paired-session-proof  — verifying the stored browser profile key
//	password-login        — password deployment with no live cookie
//	unpaired              — no usable credential: the bounded pairing screen
//	pairing               — a valid pairing fragment was consumed
//	authenticated         — the Desktop mounts
//	unavailable           — the gateway auth store is unreachable (retryable)
export type AuthGateState =
  | "checking-session"
  | "paired-session-proof"
  | "password-login"
  | "unpaired"
  | "pairing"
  | "authenticated"
  | "unavailable";

export interface AuthGateProps {
  deviceAuth: DeviceAuthClient;
  children: ReactNode;
}

const DEVICE_NAME_MAX = 80;

export function AuthGate({ deviceAuth, children }: AuthGateProps) {
  const [state, setState] = useState<AuthGateState>("checking-session");
  const [message, setMessage] = useState<string>();
  const [fragment, setFragment] = useState<string>();
  const [mode, setMode] = useState<AuthDeploymentMode>();
  const [attempt, setAttempt] = useState(0);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // Determine the deployment mode before trying any device credential. A
  // password deployment never silently proves a stored pairing key.
  useEffect(() => {
    const lifecycle: { cancelled: boolean } = { cancelled: false };
    const isCancelled = (): boolean => lifecycle.cancelled;
    void (async () => {
      let resolvedMode: AuthDeploymentMode | undefined;
      try {
        resolvedMode = await deviceAuth.getAuthMode();
        if (isCancelled()) return;
        setMode(resolvedMode);
        const hash = window.location.hash;
        if (hash) {
          history.replaceState(null, "", window.location.pathname + window.location.search);
          if (resolvedMode === "pairing") {
            try {
              parsePairingFragment(hash);
              setFragment(hash);
              setState("pairing");
              return;
            } catch {
              // Ignore invalid fragments and continue with the current session.
            }
          }
        }
        if (resolvedMode === "dev_bypass") {
          setState("authenticated");
          return;
        }
        const current = await deviceAuth.restoreSession();
        if (!isCancelled() && current !== undefined) {
          setState("authenticated");
          return;
        }
      } catch (error) {
        if (isCancelled()) return;
        if (isUnavailable(error)) {
          setState("unavailable");
          return;
        }
        if (!(error instanceof ConnectError) || error.code !== Code.Unauthenticated) {
          setState("unavailable");
          return;
        }
      }
      if (isCancelled()) return;
      if (resolvedMode === "password") {
        setState("password-login");
        return;
      }
      // No live cookie; prove the stored profile key if one exists.
      try {
        await deviceAuth.reauthenticate();
        if (!isCancelled()) setState("authenticated");
      } catch (error) {
        if (isCancelled()) return;
        if (isUnavailable(error)) {
          setState("unavailable");
          return;
        }
        setState("unpaired");
      }
    })();
    return () => {
      lifecycle.cancelled = true;
    };
  }, [attempt, deviceAuth]);

  const handlePaired = useCallback(() => {
    setState("authenticated");
  }, []);

  const handlePairingUnavailable = useCallback(() => {
    setState("unavailable");
  }, []);

  const reauthenticate = useCallback(async () => {
    setState("checking-session");
    if (mode === "password" || mode === undefined) {
      setAttempt((current) => current + 1);
      return;
    }
    try {
      await deviceAuth.reauthenticate();
      if (mounted.current) setState("authenticated");
    } catch (error) {
      if (!mounted.current) return;
      if (isUnavailable(error)) {
        setState("unavailable");
        return;
      }
      setState("unpaired");
      setMessage("This device can no longer be verified. Pair again to continue.");
    }
  }, [deviceAuth, mode]);

  const forget = useCallback(async () => {
    try {
      await deviceAuth.forget();
      await clearLocalDesktopState();
    } catch {
      // Keep the failure visible: neither a failed logout nor failed local
      // storage cleanup establishes that this browser has been forgotten.
      if (mounted.current) setMessage("This browser could not be fully cleared. Try Forget again.");
      return;
    }
    if (mounted.current) {
      setMessage(undefined);
      setState(mode === "password" ? "password-login" : "unpaired");
    }
  }, [deviceAuth, mode]);

  if (state === "authenticated") return <>{children}</>;
  return (
    <AuthGateView
      deviceAuth={deviceAuth}
      fragment={fragment ?? null}
      message={message ?? null}
      state={state}
      onForget={forget}
      onPaired={handlePaired}
      onPairingUnavailable={handlePairingUnavailable}
      onReauthenticate={() => {
        void reauthenticate();
      }}
    />
  );
}

interface AuthGateViewProps {
  deviceAuth: DeviceAuthClient;
  state: AuthGateState;
  message: string | null;
  fragment: string | null;
  onPaired: () => void;
  onPairingUnavailable: () => void;
  onReauthenticate: () => void;
  onForget: () => Promise<void>;
}

// AuthGateView renders the bounded, non-Desktop screens. Unavailable copy is
// deliberately distinct from unpaired copy: a transient outage must never
// trick the user into deleting their device key.
export function AuthGateView({
  deviceAuth,
  state,
  message,
  fragment,
  onPaired,
  onPairingUnavailable,
  onReauthenticate,
  onForget,
}: AuthGateViewProps) {
  const [deviceName, setDeviceName] = useState(defaultDeviceName());
  const [pairError, setPairError] = useState<string>();
  const [gateError, setGateError] = useState<string>();
  const [secret, setSecret] = useState<string>();
  const [fingerprint, setFingerprint] = useState<string>();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [loggingIn, setLoggingIn] = useState(false);
  const [loginError, setLoginError] = useState<string>();

  useEffect(() => {
    if (!fragment) return;
    try {
      const parsed = parsePairingFragment(fragment);
      setSecret(parsed.secret);
      setFingerprint(parsed.tlsFingerprint);
    } catch {
      setGateError("This pairing link is not valid. Scan the QR code again.");
    }
  }, [fragment]);

  async function submitPairing(event: React.SyntheticEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!secret || !fingerprint) return;
    setPairError(undefined);
    try {
      await deviceAuth.pairWithTicket({
        secret,
        tlsFingerprint: fingerprint,
        deviceName: deviceName.trim().slice(0, DEVICE_NAME_MAX) || defaultDeviceName(),
        deviceClass: "desktop",
      });
      onPaired();
    } catch (error) {
      if (isUnavailable(error)) {
        onPairingUnavailable();
        return;
      }
      setPairError(
        "Pairing failed. The ticket may have expired or been replaced — scan the QR code again.",
      );
    }
  }

  async function submitPassword(event: React.SyntheticEvent<HTMLFormElement>) {
    event.preventDefault();
    if (loggingIn || !username.trim() || !password) return;
    setLoggingIn(true);
    setLoginError(undefined);
    try {
      await deviceAuth.loginWithPassword({
        username: username.trim(),
        password,
        deviceName: defaultDeviceName(),
        deviceClass: "desktop",
      });
      setPassword("");
      onPaired();
    } catch (error) {
      setPassword("");
      if (isUnavailable(error)) {
        onPairingUnavailable();
        return;
      }
      setLoginError(
        error instanceof ConnectError && error.code === Code.ResourceExhausted
          ? "Too many attempts. Wait a moment and try again."
          : "Sign in failed. Check the username and password, then try again.",
      );
    } finally {
      setLoggingIn(false);
    }
  }

  return (
    <section className="auth-gate" data-state={state} data-testid="auth-gate">
      <h1>WorkOS</h1>
      {state === "checking-session" ? <p>Checking this device&apos;s session…</p> : null}
      {state === "paired-session-proof" ? <p>Verifying this device…</p> : null}
      {state === "password-login" ? (
        <form
          className="auth-gate-login"
          data-testid="password-login"
          onSubmit={(event) => void submitPassword(event)}
        >
          <p>Sign in to this WorkOS</p>
          <label className="auth-gate-field">
            Username
            <input
              autoComplete="username"
              name="username"
              onChange={(event) => setUsername(event.target.value)}
              required
              value={username}
            />
          </label>
          <label className="auth-gate-field">
            Password
            <input
              autoComplete="current-password"
              name="password"
              onChange={(event) => setPassword(event.target.value)}
              required
              type="password"
              value={password}
            />
          </label>
          {loginError ? (
            <p className="auth-gate-error" role="alert">
              {loginError}
            </p>
          ) : null}
          <Button disabled={loggingIn} type="submit">
            {loggingIn ? "Signing in…" : "Sign in"}
          </Button>
        </form>
      ) : null}
      {state === "unavailable" ? (
        <>
          <p>
            WorkOS is temporarily unavailable. Nothing is wrong with this device — try again in a
            moment.
          </p>
          <Button type="button" onClick={onReauthenticate}>
            Retry
          </Button>
        </>
      ) : null}
      {state === "pairing" ? (
        <form data-testid="pairing-panel" onSubmit={(event) => void submitPairing(event)}>
          {gateError ? (
            <p className="auth-gate-error" role="alert">
              {gateError}
            </p>
          ) : secret ? (
            <>
              <label className="auth-gate-field">
                Device name
                <input
                  aria-label="Device name"
                  maxLength={DEVICE_NAME_MAX}
                  onChange={(event) => {
                    setDeviceName(event.target.value);
                  }}
                  value={deviceName}
                />
              </label>
              <p className="auth-gate-hint">
                Confirm the certificate fingerprint shown by the pairing issuer matches:{" "}
                <code>{fingerprint}</code>
              </p>
              <p className="auth-gate-hint">
                This browser keeps a private key that only it can use. It never leaves this profile.
              </p>
              {pairError ? (
                <p className="auth-gate-error" role="alert">
                  {pairError}
                </p>
              ) : null}
              <Button type="submit">Pair device</Button>
            </>
          ) : null}
        </form>
      ) : null}
      {state === "unpaired" ? (
        <>
          <p>{message ?? "This browser is not yet paired with this WorkOS."}</p>
          <p className="auth-gate-hint">
            Ask the operator to show a pairing QR code (workosctl device pair) and open the link it
            prints.
          </p>
          <div className="auth-gate-actions">
            <Button type="button" onClick={onReauthenticate}>
              Try again
            </Button>
            <Button
              type="button"
              onClick={() => {
                void onForget();
              }}
            >
              Forget this browser
            </Button>
          </div>
        </>
      ) : null}
    </section>
  );
}

function defaultDeviceName(): string {
  return "Desktop browser";
}
