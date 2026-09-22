import { parsePairingFragment } from "@workos/device-auth";

export const DEPLOYMENT_SLOT = "workos.mobile-origin.v1";

export function parseDeployment(value: string): { origin: string; fragment?: string } {
  const url = new URL(value.trim());
  if (
    url.protocol !== "https:" ||
    url.username ||
    url.password ||
    (url.pathname !== "/" && !(url.pathname === "/pair" && url.hash)) ||
    url.search
  ) {
    throw new Error("Enter an HTTPS WorkOS address or pairing link.");
  }
  if (url.hash) {
    parsePairingFragment(url.hash);
    return { origin: url.origin, fragment: url.hash };
  }
  return { origin: url.origin };
}
