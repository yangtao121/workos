// The launchable mobile entry (ADR-0019 W5): mounts the shared shell
// against the configured WorkOS origin with device pairing and key storage
// behind the native wrapper. Falls back to an honest origin prompt when the
// wrapper runs on a host without a paired deployment.
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createMobileVault } from "./native.js";
import { MobileShell } from "./shell.js";
import "./app.css";

function resolveOrigin(): string {
  const configured = import.meta.env.WORKOS_MOBILE_ORIGIN as string | undefined;
  if (configured && /^https:\/\/.+/.test(configured)) {
    return configured;
  }
  const embedded =
    typeof document !== "undefined"
      ? (document.querySelector('meta[name="workos-origin"]')?.getAttribute("content") ?? "")
      : "";
  if (/^https:\/\/.+/.test(embedded)) {
    return embedded;
  }
  return window.location.origin;
}

const root = document.getElementById("root");
if (!root) throw new Error("WorkOS root element is missing");

const origin = resolveOrigin();
const mobileVault = createMobileVault();

void mobileVault.status().then((status) => {
  createRoot(root).render(
    <StrictMode>
      <MobileShell origin={origin} secureVaultReady={status.secure} vault={mobileVault} />
    </StrictMode>,
  );
});
