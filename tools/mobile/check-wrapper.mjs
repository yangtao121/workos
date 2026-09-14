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
const gradle = await readFile(resolve(root, "android/app/capacitor.build.gradle"), "utf8");
if (!gradle.includes("capacitor-secure-storage-plugin")) {
  missing.push("android secure-storage plugin link");
}
if (missing.length) {
  console.error(`Mobile wrapper software is incomplete: ${missing.join(", ")}`);
  process.exitCode = 1;
} else {
  console.log(
    "Mobile wrapper software prerequisites exist (launchable app, platform sources, secure-storage plugin link); native binaries and device acceptance still required.",
  );
}
