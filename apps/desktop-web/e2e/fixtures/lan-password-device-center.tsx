import { createRoot } from "react-dom/client";
import type { DeviceAuthClient } from "@workos/device-auth";
import { DeviceClass, type DeviceInfo } from "@workos/protocol";
import { DeviceCenter } from "../../src/DeviceCenter.js";
import "../../src/styles.css";

const device = {
  deviceId: "0198d7ea-2110-7c42-b659-c5e4d73bc301",
  name: "Fixture Desktop",
  deviceClass: DeviceClass.DESKTOP,
  revision: 3n,
  isCurrent: true,
} as DeviceInfo;
const auth = {
  getAuthMode: async () => "password" as const,
  listDevices: async () => ({ devices: [device], nextPageToken: "" }),
  getCurrentSession: async () => ({ device, sessionExpiresAt: new Date("2030-08-31T12:00:00Z") }),
  logout: async () => {},
  revokeDevice: async () => {},
} as unknown as DeviceAuthClient;

createRoot(document.getElementById("fixture-root")!).render(
  <main className="desktop-shell" style={{ width: "100vw", height: "100vh" }}>
    <section className="workos-window" style={{ left: 700, top: 90, width: 560, height: 480 }}>
      <header>
        <span className="window-identity">
          <strong>Device Center</strong>
        </span>
      </header>
      <DeviceCenter deviceAuth={auth} />
    </section>
  </main>,
);
