import { afterEach, describe, expect, it, vi } from "vitest";
import { byteLength, openReasonText, passwordProblem } from "./api";
import { openAvailability } from "./AccountStep";
import { FIRST_WELCOME, STEPS, STEP_COUNT, nextWelcome, prevWelcome, welcomeGuard, welcomeStep } from "./steps";
import { FALLBACK_ZONES, offsetLabel, searchZones, zoneEntries, zoneNames } from "./zones";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("zone list", () => {
  const at = new Date("2026-01-15T12:00:00Z");
  const entries = zoneEntries(["UTC", "Asia/Tokyo", "Asia/Kolkata", "America/New_York", "America/Argentina/Buenos_Aires", "Australia/Adelaide", "Pacific/Honolulu"], at);
  const names = (q: string) => searchZones(entries, q).map((e) => e.name);

  it("labels each zone with its current offset", () => {
    expect(offsetLabel("Asia/Tokyo", at)).toBe("UTC+09:00");
    expect(offsetLabel("Asia/Kolkata", at)).toBe("UTC+05:30");
    expect(offsetLabel("America/New_York", at)).toBe("UTC-05:00");
    expect(offsetLabel("America/New_York", new Date("2026-07-15T12:00:00Z"))).toBe("UTC-04:00");
    expect(offsetLabel("UTC", at)).toBe("UTC");
    expect(offsetLabel("Not/AZone", at)).toBe("");
    expect(entries.find((e) => e.name === "Asia/Tokyo")?.label).toBe("Asia/Tokyo (UTC+09:00)");
    expect(entries.find((e) => e.name === "America/Argentina/Buenos_Aires")?.label).toBe("America/Argentina/Buenos Aires (UTC-03:00)");
  });

  it("searches by name, ignoring case and underscores", () => {
    expect(names("tokyo")).toEqual(["Asia/Tokyo"]);
    expect(names("buenos aires")).toEqual(["America/Argentina/Buenos_Aires"]);
    expect(names("america new")).toEqual(["America/New_York"]);
    expect(names("")).toHaveLength(entries.length);
    expect(names("zzz")).toEqual([]);
  });

  it("searches by offset in the usual spellings, as a whole offset", () => {
    expect(names("+9")).toEqual(["Asia/Tokyo"]);
    expect(names("UTC+09:00")).toEqual(["Asia/Tokyo"]);
    expect(names("+5:30")).toEqual(["Asia/Kolkata"]);
    expect(names("-5")).toEqual(["America/New_York"]);
    expect(names("-10")).toEqual(["Pacific/Honolulu"]);
    // +9 must not match +9:30 (Adelaide is +10:30 in January, and +10:30 does not contain +9).
    expect(names("+10:30")).toEqual(["Australia/Adelaide"]);
  });

  it("takes a bare utc or gmt as the zero offset, not as every zone", () => {
    expect(names("utc")).toEqual(["UTC"]);
    expect(names("gmt")).toEqual(["UTC"]);
    expect(names("UTC+9")).toEqual(["Asia/Tokyo"]);
  });

  it("lists the browser's zones with UTC first, or the bundled list when the browser cannot list them", () => {
    const all = zoneNames();
    expect(all[0]).toBe("UTC");
    expect(all).toContain("Asia/Tokyo");
    const f = Intl as unknown as { supportedValuesOf?: unknown };
    const orig = f.supportedValuesOf;
    f.supportedValuesOf = undefined;
    try {
      const fb = zoneNames();
      expect(fb).toEqual(expect.arrayContaining([...FALLBACK_ZONES]));
      expect(fb[0]).toBe("UTC");
    } finally {
      f.supportedValuesOf = orig;
    }
  });
});

describe("password rules", () => {
  it("counts bytes and refuses the example value", () => {
    expect(byteLength("é")).toBe(2);
    expect(passwordProblem("abcd")).toMatch(/at least 5/);
    expect(passwordProblem("abcde")).toBeNull();
    expect(passwordProblem("change-me")).toMatch(/example/);
    expect(passwordProblem("x".repeat(257))).toMatch(/too long/);
    expect(passwordProblem("é".repeat(128))).toBeNull();
    expect(passwordProblem("é".repeat(129))).toMatch(/too long/);
  });
});

describe("open mode availability", () => {
  it("works from here, or cannot work and says why", () => {
    expect(openAvailability({ reason: null })).toEqual({ ok: true, why: null });
    expect(openAvailability({ reason: "forwarded" })).toMatchObject({ ok: false, why: openReasonText("forwarded") });
    expect(openAvailability({ reason: "host" }).why).toMatch(/KIPPLE_ALLOWED_HOSTS/);
  });
});

describe("step registry", () => {
  it("has six steps in order, the account first (before sign-in) and five after", () => {
    expect(STEPS.map((s) => s.id)).toEqual(["account", "timezone", "theme", "import", "feeds", "finish"]);
    expect(STEPS.map((s) => s.n)).toEqual([1, 2, 3, 4, 5, 6]);
    expect(STEP_COUNT).toBe(6);
    expect(STEPS.filter((s) => s.phase === "setup").map((s) => s.id)).toEqual(["account"]);
    expect(FIRST_WELCOME.id).toBe("timezone");
  });

  it("guards the address: an unknown step, or one from before sign-in, goes to the first", () => {
    expect(welcomeGuard("theme")).toBeNull();
    expect(welcomeGuard(undefined)).toBe("/welcome/timezone");
    expect(welcomeGuard("nope")).toBe("/welcome/timezone");
    expect(welcomeGuard("account")).toBe("/welcome/timezone");
    expect(welcomeStep("account")).toBeNull();
  });

  it("walks forward and back, with no way back from the first and none forward from the last", () => {
    expect(nextWelcome("timezone")?.id).toBe("theme");
    expect(nextWelcome("feeds")?.id).toBe("finish");
    expect(nextWelcome("finish")).toBeNull();
    expect(prevWelcome("timezone")).toBeNull();
    expect(prevWelcome("finish")?.id).toBe("feeds");
  });

  it("only lets steps 2 to 5 be skipped", () => {
    expect(STEPS.filter((s) => s.skippable).map((s) => s.id)).toEqual(["timezone", "theme", "import", "feeds"]);
  });
});
