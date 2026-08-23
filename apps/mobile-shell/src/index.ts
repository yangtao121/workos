export type DeviceClass = "phone" | "tablet" | "foldable" | "desktop";

export function classifyDevice(width: number, separatedFold = false): DeviceClass {
  if (separatedFold) return "foldable";
  if (width < 600) return "phone";
  if (width < 1024) return "tablet";
  return "desktop";
}
