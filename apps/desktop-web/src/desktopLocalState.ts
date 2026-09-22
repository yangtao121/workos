import { createLayoutStore } from "@workos/adaptive-shell";
import { clearDesktopProjection } from "./sharedDesktop.js";
import { clearSessionContinuity } from "./sessionContinuity.js";

// One layout store per browser app. Content journals have a separate database;
// both are removed only after confirmed logout/revoke or explicit Forget.
export const layoutStore = createLayoutStore();

export async function clearLocalDesktopState(): Promise<void> {
  clearDesktopProjection();
  try {
    sessionStorage.removeItem("workos.activeProjectId");
  } catch {
    // An unavailable sessionStorage cannot supply a stale project next time.
  }
  // Calling this immediately fences live session callbacks, before either
  // asynchronous database transaction has finished.
  const journals = clearSessionContinuity();
  await Promise.all([journals, layoutStore.clearAll()]);
}
