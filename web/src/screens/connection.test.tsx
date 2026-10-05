import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { axe } from "vitest-axe";
import { makeQueryClient } from "@/App";
import type { SettingMeta } from "@/api/admin";
import { json, mockFetch } from "@/test/mockApi";
import { ConnectionSection, parseList } from "./ConnectionSection";

// Settings, Account & Devices, "Address and access": the public URL, the allowed host names, the trusted proxies and
// Cloudflare Access, each saved on its own through PATCH /api/settings.

const meta = (key: string, label: string, kind: SettingMeta["kind"], value: unknown, def: unknown): SettingMeta => ({
  key,
  label,
  kind,
  value,
  default: def,
  description: `About ${label}`,
  group: "connection",
  surface: "settings",
});

function world(values: Record<string, unknown> = {}) {
  const v: Record<string, unknown> = {
    "server.public_url": "",
    "security.allowed_hosts": [],
    "security.trusted_proxies": [],
    "security.cloudflare_access": {},
    ...values,
  };
  const body = () => ({
    settings: [
      meta("server.public_url", "Public URL", "text", v["server.public_url"], ""),
      meta("security.allowed_hosts", "Allowed host names", "json", v["security.allowed_hosts"], []),
      meta("security.trusted_proxies", "Trusted proxies", "json", v["security.trusted_proxies"], []),
      meta("security.cloudflare_access", "Cloudflare Access", "json", v["security.cloudflare_access"], {}),
    ],
    values: v,
  });
  return { v, body };
}

function show(w: ReturnType<typeof world>, patch?: (body: Record<string, unknown>) => Response) {
  const api = mockFetch({
    "GET /api/settings": () => json(w.body()),
    "GET /api/auth/me": () => json({ username: "reader" }),
    "PATCH /api/settings": (_u, init) => {
      const b = JSON.parse(String(init?.body)) as Record<string, unknown>;
      const custom = patch?.(b);
      if (custom) return custom;
      Object.assign(w.v, b);
      return json(w.body());
    },
  });
  const r = render(
    <QueryClientProvider client={makeQueryClient({ retry: false })}>
      <ConnectionSection />
    </QueryClientProvider>,
  );
  return { ...api, ...r };
}

const patches = (calls: { method: string; init?: RequestInit }[]) =>
  calls.filter((c) => c.method === "PATCH").map((c) => JSON.parse(String(c.init?.body)) as Record<string, unknown>);

describe("parseList", () => {
  it("takes one entry per line, commas or spaces, and drops blanks", () => {
    expect(parseList(" nas.local\n\nrss.example.com,  *.example.org \n")).toEqual(["nas.local", "rss.example.com", "*.example.org"]);
    expect(parseList("   ")).toEqual([]);
  });
});

describe("Address and access", () => {
  it("shows the saved values, says changes apply at once, and passes axe", async () => {
    const w = world({ "server.public_url": "https://rss.example.com", "security.allowed_hosts": ["nas.local", "*.example.org"], "security.cloudflare_access": { team_domain: "myteam.cloudflareaccess.com", aud: "abc" } });
    const { container } = show(w);
    expect(await screen.findByLabelText("Public URL")).toHaveValue("https://rss.example.com");
    expect(screen.getByLabelText("Allowed host names")).toHaveValue("nas.local\n*.example.org");
    expect(screen.getByLabelText("Trusted proxies")).toHaveValue("");
    expect(screen.getByLabelText("Team domain")).toHaveValue("myteam.cloudflareaccess.com");
    expect(screen.getByText(/On: Kipple checks the Access token of/)).toBeInTheDocument();
    expect(screen.getByText(/Changes apply at once; Kipple does not need a restart/)).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("saves each one on its own: the address trimmed, lists as lists, Access as one value", async () => {
    const { calls } = show(world());
    const user = userEvent.setup();
    const url = await screen.findByLabelText("Public URL");
    // Nothing changed: nothing to save.
    expect(screen.getByRole("button", { name: "Save Public URL" })).toBeDisabled();
    await user.type(url, " https://rss.example.com ");
    await user.keyboard("{Enter}");
    await screen.findByRole("button", { name: "Remove the public url" });

    const hosts = screen.getByLabelText("Allowed host names");
    await user.type(hosts, "nas.local{Enter}rss.example.com");
    await user.click(screen.getByRole("button", { name: "Save Allowed host names" }));

    const proxies = screen.getByLabelText("Trusted proxies");
    await user.type(proxies, "192.0.2.10, 198.51.100.0/24");
    await user.click(screen.getByRole("button", { name: "Save Trusted proxies" }));

    const access = screen.getByRole("group", { name: "Cloudflare Access" });
    await user.type(within(access).getByLabelText("Team domain"), "myteam.cloudflareaccess.com");
    await user.type(within(access).getByLabelText("Application audience (AUD) tag"), " abc ");
    await user.click(within(access).getByRole("button", { name: "Save Cloudflare Access" }));
    await within(access).findByRole("button", { name: "Turn off Cloudflare Access" });
    await user.click(within(access).getByRole("button", { name: "Turn off Cloudflare Access" }));
    await within(access).findByText(/^Off/);

    expect(patches(calls)).toEqual([
      { "server.public_url": "https://rss.example.com" },
      { "security.allowed_hosts": ["nas.local", "rss.example.com"] },
      { "security.trusted_proxies": ["192.0.2.10", "198.51.100.0/24"] },
      { "security.cloudflare_access": { team_domain: "myteam.cloudflareaccess.com", aud: "abc" } },
      { "security.cloudflare_access": {} },
    ]);
  });

  it("shows the server's reason next to the field, and the Access-in-use refusal", async () => {
    const w = world({ "security.cloudflare_access": { team_domain: "myteam.cloudflareaccess.com", aud: "abc" } });
    show(w, (b) => {
      if ("security.trusted_proxies" in b)
        return json({ error: "invalid_settings", message: "invalid settings", keys: ["security.trusted_proxies"], issues: [{ key: "security.trusted_proxies", message: 'invalid address range "0.0.0.0/0": it covers every address' }] }, 400);
      if ("security.cloudflare_access" in b) return json({ error: "access_in_use", message: "this account has no web password and signs in through Cloudflare Access: set a web password first" }, 409);
      return undefined as unknown as Response;
    });
    const user = userEvent.setup();
    const proxies = await screen.findByLabelText("Trusted proxies");
    await user.type(proxies, "0.0.0.0/0");
    await user.click(screen.getByRole("button", { name: "Save Trusted proxies" }));
    expect(await screen.findByText(/it covers every address/)).toBeInTheDocument();
    expect(proxies).toHaveAttribute("aria-invalid", "true");
    expect(proxies).toHaveValue("0.0.0.0/0");

    const access = screen.getByRole("group", { name: "Cloudflare Access" });
    await user.click(within(access).getByRole("button", { name: "Turn off Cloudflare Access" }));
    expect(await within(access).findByText(/Set a web password first/)).toBeInTheDocument();
    expect(within(access).getByText(/On: Kipple checks/)).toBeInTheDocument();
  });
});
