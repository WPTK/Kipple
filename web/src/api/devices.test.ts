import { describe, expect, it } from "vitest";
import { deviceKind } from "./devices";

describe("deviceKind", () => {
  it("tells phones, tablets and computers apart", () => {
    expect(deviceKind("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/604.1")).toBe("phone");
    expect(deviceKind("Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X) Safari/604.1")).toBe("tablet");
    expect(deviceKind("Mozilla/5.0 (Linux; Android 14; Pixel 8) Chrome/120 Mobile Safari/537.36")).toBe("phone");
    expect(deviceKind("Mozilla/5.0 (Linux; Android 14; SM-X900) Chrome/120 Safari/537.36")).toBe("tablet");
    expect(deviceKind("Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120 Safari/537.36")).toBe("computer");
  });
});
