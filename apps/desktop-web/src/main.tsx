import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { DeviceAuthClient } from "@workos/device-auth";
import { AuthGate } from "./AuthGate.js";
import { Desktop } from "./Desktop.js";
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("WorkOS root element is missing");

// The device auth client is a trusted-shell singleton: it holds the browser
// profile key handle and never crosses into app surfaces.
const deviceAuth = new DeviceAuthClient(window.location.origin);

createRoot(root).render(
  <StrictMode>
    <AuthGate deviceAuth={deviceAuth}>
      <Desktop deviceAuth={deviceAuth} />
    </AuthGate>
  </StrictMode>,
);
