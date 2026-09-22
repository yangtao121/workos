import { describe, expect, it } from "vitest";
import { parseDeployment } from "./deployment.js";

describe("mobile deployment entry", () => {
  it("keeps only a canonical public HTTPS origin", () => {
    expect(parseDeployment(" https://WORKOS.fixture:8443/ ")).toEqual({
      origin: "https://workos.fixture:8443",
    });
  });
  it("refuses HTTP, URL credentials, query tickets, paths and malformed fragments", () => {
    for (const value of [
      "http://workos.fixture",
      "https://name:password@workos.fixture",
      "https://workos.fixture/?ticket=secret",
      "https://workos.fixture/path",
      "https://workos.fixture/pair",
      "https://workos.fixture/#bad-ticket",
      "not a URL",
    ]) {
      expect(() => parseDeployment(value)).toThrow();
    }
  });
  it("accepts the canonical /pair link emitted by Device Center and workosctl", () => {
    const fragment = `#v=1&t=${"A".repeat(43)}&fp=sha256:${"a".repeat(64)}`;
    expect(parseDeployment(`https://workos.fixture/pair${fragment}`)).toEqual({
      origin: "https://workos.fixture",
      fragment,
    });
    expect(() => parseDeployment(`https://workos.fixture/another-path${fragment}`)).toThrow();
  });
});
