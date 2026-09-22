import { useState } from "react";
import { Capacitor } from "@capacitor/core";
import { DEPLOYMENT_SLOT, parseDeployment } from "./deployment.js";
import { DeploymentForm } from "./DeploymentForm.js";
import type { MobileVault } from "./native.js";
import { MobileShell } from "./shell.js";

function initialDeployment(): { origin: string } | undefined {
  const configured = import.meta.env.WORKOS_MOBILE_ORIGIN as string | undefined;
  const embedded = document.querySelector('meta[name="workos-origin"]')?.getAttribute("content");
  try {
    const saved = Capacitor.isNativePlatform() ? localStorage.getItem(DEPLOYMENT_SLOT) : undefined;
    const value = saved || configured || embedded;
    if (value) return { origin: parseDeployment(value).origin };
  } catch {
    return undefined;
  }
  // Browser mount is already served by its deployment. Native localhost is
  // the packaged assets, so first launch requires an explicit HTTPS address.
  return Capacitor.isNativePlatform() ? undefined : { origin: window.location.origin };
}

export function MobileEntry(props: { vault: MobileVault; secureVaultReady: boolean }) {
  const [deployment, setDeployment] = useState<{ origin: string; fragment?: string } | undefined>(
    initialDeployment,
  );
  const [configurationError, setConfigurationError] = useState("");
  if (!deployment)
    return (
      <div className="workos-mobile" data-testid="mobile-setup">
        <header className="mobile-topbar">
          <span className="mobile-brand">WorkOS</span>
        </header>
        <main className="mobile-content">
          <section className="mobile-card">
            <h1>Connect to WorkOS</h1>
            <p>Enter your server address, or paste a pairing link from Device Center.</p>
            <DeploymentForm
              onConnect={(origin, fragment) => {
                try {
                  // Only the public origin persists here. Pairing tickets stay in memory.
                  localStorage.setItem(DEPLOYMENT_SLOT, origin);
                  setConfigurationError("");
                  setDeployment(fragment ? { origin, fragment } : { origin });
                } catch {
                  setConfigurationError("Could not save this server. Try again.");
                }
              }}
            />
            {configurationError ? <p role="alert">{configurationError}</p> : null}
          </section>
        </main>
      </div>
    );
  return (
    <MobileShell
      key={deployment.origin}
      origin={deployment.origin}
      initialPairingFragment={deployment.fragment}
      secureVaultReady={props.secureVaultReady}
      vault={props.vault}
      onChangeDeployment={
        Capacitor.isNativePlatform()
          ? () => {
              setDeployment(undefined);
            }
          : undefined
      }
    />
  );
}
