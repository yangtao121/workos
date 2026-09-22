// The launchable mobile entry (ADR-0019 W5): mounts the shared shell
// against the configured WorkOS origin with device pairing and key storage
// behind the native wrapper. Falls back to an honest origin prompt when the
// wrapper runs on a host without a paired deployment.
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createMobileVault } from "./native.js";
import { MobileEntry } from "./MobileEntry.js";
import "./app.css";

const root = document.getElementById("root");
if (!root) throw new Error("WorkOS root element is missing");

const mobileVault = createMobileVault();

void mobileVault.status().then((status) => {
  createRoot(root).render(
    <StrictMode>
      <MobileEntry secureVaultReady={status.secure} vault={mobileVault} />
    </StrictMode>,
  );
});
