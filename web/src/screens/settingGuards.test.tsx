import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { SettingMeta } from "@/api/admin";
import { settingWarning } from "@/lib/settingGuards";
import { Segmented } from "@/ui/segmented";
import { json, mockFetch } from "@/test/mockApi";
import { PRESETS } from "./SettingsScreen";
import { SettingField } from "./SettingField";
import { ImageCachePanel } from "./ImageCachePanel";

afterEach(() => vi.restoreAllMocks());

const cache = (value: number): SettingMeta =>
  ({ key: "imgproxy.cache_mb", label: "Image cache size", description: "", kind: "int", value, default: 1024, min: 0, max: 20480, step: 64, unit: "MB", group: "images", surface: "settings" }) as SettingMeta;
const retention = (value: number): SettingMeta =>
  ({
    key: "retention.default",
    label: "Articles to keep per feed",
    description: "",
    kind: "enum",
    value,
    default: 250,
    options: [50, 100, 250, 500, 1000, 0].map((v) => ({ value: v, label: v === 0 ? "Unlimited" : String(v) })),
    group: "library",
    surface: "settings",
  }) as SettingMeta;

const restoreDays = (value: number): SettingMeta =>
  ({ key: "retention.restore_days", label: "Days you can restore removed articles", description: "", kind: "int", value, default: 90, min: 0, max: 365, step: 1, unit: "days", group: "library", surface: "settings" }) as SettingMeta;

function mount(meta: SettingMeta, presets?: (typeof PRESETS)[string]) {
  const api = mockFetch({ "PATCH /api/settings": (_u, init) => json({ settings: [meta], values: JSON.parse(String(init?.body)) }) });
  render(
    <QueryClientProvider client={new QueryClient()}>
      <SettingField meta={meta} presets={presets} />
    </QueryClientProvider>,
  );
  return api.calls;
}

describe("settingWarning", () => {
  it("asks for Off and for any lower image cache cap, not for a higher one", () => {
    expect(settingWarning("imgproxy.cache_mb", 2048, 0)?.title).toMatch(/off/i);
    expect(settingWarning("imgproxy.cache_mb", 2048, 512)?.body).toMatch(/removes the oldest cached images now/);
    expect(settingWarning("imgproxy.cache_mb", 512, 2048)).toBeNull();
    expect(settingWarning("imgproxy.cache_mb", 0, 256)).toBeNull();
    expect(settingWarning("imgproxy.cache_mb", 512, 512)).toBeNull();
  });
  it("asks when retention keeps fewer articles, treating 0 as unlimited", () => {
    expect(settingWarning("retention.default", 250, 100)).not.toBeNull();
    expect(settingWarning("retention.default", 0, 1000)).not.toBeNull();
    expect(settingWarning("retention.default", 250, 500)).toBeNull();
    expect(settingWarning("retention.default", 250, 0)).toBeNull();
  });
  it("asks when restore days are lowered, not when raised", () => {
    const w = settingWarning("retention.restore_days", 90, 30);
    expect(w?.body).toBe("Restore stubs older than 30 days are removed tonight and can't come back.");
    expect(settingWarning("retention.restore_days", 90, 0)?.body).toMatch(/Every restore stub/);
    expect(settingWarning("retention.restore_days", 30, 90)).toBeNull();
    expect(settingWarning("retention.restore_days", 0, 30)).toBeNull();
  });
  it("has nothing to say about other settings", () => {
    expect(settingWarning("imgproxy.mode", "all", "http_only")).toBeNull();
    expect(settingWarning("library.auto_read_days", 90, 30)).toBeNull();
  });
});

describe("Segmented explicit", () => {
  it("arrow keys move focus only; Enter or Space on the focused option picks it", async () => {
    const onChange = vi.fn();
    render(<Segmented legend="Size" value="a" explicit options={[{ value: "a", label: "A" }, { value: "b", label: "B" }, { value: "c", label: "C" }]} onChange={onChange} />);
    const user = userEvent.setup();
    screen.getByRole("radio", { name: "A" }).focus();
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("radio", { name: "B" })).toHaveFocus();
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("radio", { name: "C" })).toHaveFocus();
    await user.keyboard("{ArrowRight}"); // wraps
    expect(screen.getByRole("radio", { name: "A" })).toHaveFocus();
    await user.keyboard("{ArrowLeft}");
    expect(screen.getByRole("radio", { name: "C" })).toHaveFocus();
    expect(onChange).not.toHaveBeenCalled();
    await user.keyboard("{Enter}");
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenLastCalledWith("c");
  });
});

describe("Destructive settings", () => {
  it("arrowing across the cache presets (through Off) sends nothing; Off asks first and then purges on confirm", async () => {
    const calls = mount(cache(2048), PRESETS["imgproxy.cache_mb"]);
    const user = userEvent.setup();
    screen.getByRole("radio", { name: "2 GB" }).focus();
    await user.keyboard("{ArrowRight}{ArrowRight}{ArrowRight}{ArrowRight}");
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(0);
    screen.getByRole("radio", { name: "Off" }).focus();
    await user.keyboard("{Enter}");
    const dlg = await screen.findByRole("dialog");
    expect(dlg).toHaveTextContent(/removes every cached image now/);
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "Turn off and clear" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1));
    expect(JSON.parse(String(calls.find((c) => c.method === "PATCH")?.init?.body))).toEqual({ "imgproxy.cache_mb": 0 });
  });

  it("a lower cap asks first; Cancel sends nothing; a higher cap needs no question", async () => {
    const calls = mount(cache(1024), PRESETS["imgproxy.cache_mb"]);
    const user = userEvent.setup();
    await user.click(screen.getByRole("radio", { name: "512 MB" }));
    const dlg = await screen.findByRole("dialog");
    expect(dlg).toHaveTextContent(/removes the oldest cached images now/);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(0);
    await user.click(screen.getByRole("radio", { name: "2 GB" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("lowering retention.default asks first", async () => {
    const calls = mount(retention(250));
    const user = userEvent.setup();
    await user.selectOptions(screen.getByRole("combobox", { name: "Articles to keep per feed" }), "50");
    expect(await screen.findByRole("dialog")).toHaveTextContent(/removed now/);
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "Keep fewer" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1));
  });
});

describe("ImageCachePanel", () => {
  const stats = { enabled: true, mode: "all", cache_mb: 1024, max_bytes: 1024 ** 3, used_bytes: 1, entries: 1, neg_entries: 0, thumbnails: 0, hits: 0, misses: 0, evictions: 0, failures: 0, since: 1, oldest_access_at: null, disk_free_bytes: 10 ** 11, disk_floor_bytes: 1, low_disk: false };

  it("fetches once when it opens (no second fetch 800 ms later) and again only when the cap or mode changes", async () => {
    const { calls } = mockFetch({ "GET /api/imgcache": () => json(stats) });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrap = (watch: string) => (
      <QueryClientProvider client={client}>
        <ImageCachePanel watch={watch} />
      </QueryClientProvider>
    );
    const { rerender } = render(wrap("1024|all"));
    await screen.findByTestId("imgcache-card");
    await act(async () => void (await new Promise((r) => setTimeout(r, 1100))));
    expect(calls.filter((c) => c.url.pathname === "/api/imgcache")).toHaveLength(1);
    rerender(wrap("512|all"));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/imgcache")).toHaveLength(2));
  });
});

describe("restore days guard", () => {
  it("asks on Reset when the default is lower, and does nothing until confirmed", async () => {
    const calls = mount({ ...restoreDays(180), default: 90 } as SettingMeta);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /Reset .* to default/ }));
    const dlg = await screen.findByRole("dialog", { name: "Keep restore stubs for fewer days?" });
    expect(dlg).toHaveTextContent("older than 90 days are removed tonight");
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);
  });
});
