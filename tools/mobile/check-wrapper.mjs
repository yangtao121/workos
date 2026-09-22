import { access, readFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve("apps/mobile-shell");
const config = JSON.parse(await readFile(resolve(root, "capacitor.config.json"), "utf8"));
if (config.appId !== "dev.workos.mobile" || config.webDir !== "dist") {
  throw new Error("Mobile wrapper configuration is invalid.");
}
const required = [
  "dist/index.html",
  "android/app/src/main/AndroidManifest.xml",
  "ios/App/App.xcodeproj/project.pbxproj",
];
const missing = [];
for (const file of required) {
  try {
    await access(resolve(root, file));
  } catch {
    missing.push(file);
  }
}
// The secure storage plugin must actually be linked into the native build:
// the device key vault is only real when the platform project depends on it.
const swift = await readFile(resolve(root, "ios/App/CapApp-SPM/Package.swift"), "utf8");
if (!swift.includes("CapacitorSecureStoragePlugin")) missing.push("iOS secure-storage plugin link");
const activity = await readFile(
  resolve(root, "android/app/src/main/java/dev/workos/mobile/MainActivity.java"),
  "utf8",
);
if (!activity.includes("registerPlugin(WorkOSSecureStoragePlugin.class)"))
  missing.push("android fail-closed Keystore plugin registration");
await access(resolve(root, "android/app/src/main/java/dev/workos/mobile/DeviceKeyVault.java"));
if (missing.length) {
  console.error(`Mobile wrapper software is incomplete: ${missing.join(", ")}`);
  process.exitCode = 1;
} else {
  console.log(
    "Mobile wrapper software prerequisites exist (launchable app, platform sources, secure-storage plugin link); native binaries and device acceptance still required.",
  );
}
