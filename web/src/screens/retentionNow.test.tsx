import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { json, mockFetch } from "@/test/mockApi";
import { ApplyRetentionNow } from "./RetentionDialog";

afterEach(() => vi.restoreAllMocks());

function mount(preview: { articles: number; feeds: number }, apply: () => Response = () => json({ run_id: "1", total: 2 }, 202)) {
  const api = mockFetch({
    "GET /api/retention/preview": () => json(preview),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "POST /api/retention/apply": apply,
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ApplyRetentionNow />
    </QueryClientProvider>,
  );
  return api;
}

describe("Apply retention now", () => {
  it("says how many articles go before it removes them", async () => {
    const api = mount({ articles: 120, feeds: 3 });
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Apply retention now" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText(/About 120 articles from 3 feeds will be removed now/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Remove 120 articles" }));
    expect(api.calls.filter((c) => c.method === "POST" && c.url.pathname === "/api/retention/apply")).toHaveLength(1);
  });

  it("says nothing would be removed and offers no remove button", async () => {
    const api = mount({ articles: 0, feeds: 0 });
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Apply retention now" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("Every feed is already within its limit, so nothing is removed.")).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: /^Remove/ })).toBeNull();
    expect(api.calls.some((c) => c.method === "POST")).toBe(false);
  });
});
