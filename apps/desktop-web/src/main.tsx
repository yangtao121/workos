// LAN HTTP is not a secure context, so randomUUID is missing there.
// getRandomValues still works and is what the desktop idempotency keys need.
if (typeof crypto.randomUUID !== "function") {
  crypto.randomUUID = () => {
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    bytes[6] = (bytes[6]! & 0x0f) | 0x40;
    bytes[8] = (bytes[8]! & 0x3f) | 0x80;
    const hex = [...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  };
}

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
