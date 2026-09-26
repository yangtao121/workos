import { createRoot } from "react-dom/client";
import type { WorkOSClients } from "@workos/agent-sdk";
import { GreenfieldApp } from "../../src/GreenfieldApp.js";
import "../../src/styles.css";

const clients = {
  nativeSessions: {
    openGreenfieldDisplay: () => Promise.reject(new Error("fixture display unavailable")),
  },
} as unknown as WorkOSClients;
const root = document.getElementById("fixture-root");
if (!root) throw new Error("missing fixture root");
createRoot(root).render(
  <main className="desktop-shell" style={{ width: "100vw", height: "100vh" }}>
    <section className="workos-window" style={{ left: 160, top: 80, width: 1120, height: 740 }}>
      <header>
        <span className="window-identity">
          <strong>WorkOS Code</strong>
        </span>
      </header>
      <div className="native-app">
        <GreenfieldApp clients={clients} sessionId="01999999-9999-7999-8999-000000000010" />
      </div>
    </section>
  </main>,
);
