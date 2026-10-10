import { describe, expect, it, vi } from "vitest";
import type { About } from "@/api/about";
import { debugText, startedText, uptimeText, type ClientFacts } from "./aboutText";
import { reloadForUpdate, serverRebuilt } from "./buildInfo";

it.each([
  ["a UTC time becomes a local one with a year, not raw ISO", "2026-10-01T13:00:00Z", (s: string) => !s.includes("T13:00") && /2026/.test(s)],
  ["text that is not a time is shown as it is", "garbage", (s: string) => s === "garbage"],
])("startedText: %s", (_name, input, ok) => {
  expect(ok(startedText(input))).toBe(true);
});

describe("serverRebuilt", () => {
  it("is true only when the server names a build that is not this bundle's", () => {
    expect(serverRebuilt("bbb", "aaa")).toBe(true);
    expect(serverRebuilt("aaa", "aaa")).toBe(false);
  });
  it("never says so for a development bundle, or about a server that reports no build", () => {
    expect(serverRebuilt("bbb", "dev")).toBe(false);
    expect(serverRebuilt("", "aaa")).toBe(false);
    expect(serverRebuilt(undefined, "aaa")).toBe(false);
  });
});

describe("reloadForUpdate", () => {
  it("asks a waiting worker to take over, then reloads", async () => {
    const postMessage = vi.fn();
    const reload = vi.fn();
    await reloadForUpdate(reload, { getRegistration: async () => ({ waiting: { postMessage } }) as unknown as ServiceWorkerRegistration });
    expect(postMessage).toHaveBeenCalledWith({ type: "skip-waiting" });
    expect(reload).toHaveBeenCalledOnce();
  });

  it("reloads anyway with no worker, or when asking it throws", async () => {
    const a = vi.fn();
    await reloadForUpdate(a, undefined);
    expect(a).toHaveBeenCalledOnce();
    const b = vi.fn();
    await reloadForUpdate(b, { getRegistration: async () => Promise.reject(new Error("no")) });
    expect(b).toHaveBeenCalledOnce();
  });
});

const about: About = {
  version: "v0.5.0-beta.1",
  commit: "0123456789abcdef0123456789abcdef01234567",
  build_date: "2026-10-01T12:00:00Z",
  go_version: "go1.27.0",
  os_arch: "linux/arm64",
  schema_version: 9,
  schema_latest: 9,
  sqlite_version: "3.50.0",
  started_at: "2026-10-01T13:00:00Z",
  uptime_s: 3 * 3600 + 12 * 60 + 5,
  data_dir_writable: true,
  tz: "America/New_York",
  auth_mode: "password",
  access_enabled: false,
  public_url_set: true,
  web_build: "abc123def4",
};
const client: ClientFacts = { bundleVersion: "v0.5.0-beta.1", bundleBuild: "abc123def4", workerShell: "1759000000000-abc", standalone: true, userAgent: "TestBrowser/1.0" };

describe("debug text", () => {
  it("is a plain block with the server's and the browser's facts", () => {
    const t = debugText(about, client);
    expect(t.split("\n")[0]).toBe("Kipple debug info");
    for (const line of [
      "Version: v0.5.0-beta.1",
      "Commit: 0123456789abcdef0123456789abcdef01234567",
      "Built: 2026-10-01T12:00:00Z",
      "Go: go1.27.0 (linux/arm64)",
      "Database schema: 9",
      "SQLite: 3.50.0",
      `Running for: 3 h 12 min (since ${startedText("2026-10-01T13:00:00Z")})`,
      "Data folder writable: yes",
      "Sign-in: password",
      "Cloudflare Access validation: off",
      "Public URL set: yes",
      "Web build on the server: abc123def4",
      "Web build in this page: v0.5.0-beta.1 (abc123def4)",
      "Service worker: 1759000000000-abc",
      "Opened as: installed app",
      "Browser: TestBrowser/1.0",
      "Started (UTC): 2026-10-01T13:00:00Z",
    ])
      expect(t).toContain(line);
  });

  it("says when the database is behind what the build expects", () => {
    expect(debugText({ ...about, schema_version: 7 }, client)).toContain("Database schema: 7 (this build expects 9)");
  });

  it("formats uptime with its two biggest units", () => {
    expect(uptimeText(45)).toBe("45 s");
    expect(uptimeText(600)).toBe("10 min");
    expect(uptimeText(4 * 86400 + 2 * 3600 + 59)).toBe("4 d 2 h");
    expect(uptimeText(-5)).toBe("0 s");
  });
});
