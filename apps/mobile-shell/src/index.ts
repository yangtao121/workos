// The mobile shell's device-class contract is now the shared adaptive-shell
// contract: one mapping from the canonical proto DeviceClass to the shell
// layout classes, with no second string enum that can drift.
export {
  classifyDevice,
  deviceClassFromProto,
  protoFromDeviceClass,
  type UiDeviceClass as DeviceClass,
} from "@workos/adaptive-shell";
