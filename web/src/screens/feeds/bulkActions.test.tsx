import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Feed } from "@/api/types";
import { mockFetch } from "@/test/mockApi";
import { DeleteDialog, deleteCopy } from "./BulkActions";

afterEach(() => vi.unstubAllGlobals());

const feed = (id: string): Feed => ({ id, title: `Feed ${id}`, folder_id: "1", status: "ok", unread: 0 }) as Feed;

describe("the bulk delete dialog", () => {
  it("keeps its total fixed while the live selection shrinks as feeds are deleted", async () => {
    let release: () => void = () => undefined;
    const held = new Promise<void>((r) => (release = r));
    mockFetch({
      "DELETE /api/feeds/1": () => new Response(null, { status: 204 }),
      "DELETE /api/feeds/2": async () => (await held, new Response(null, { status: 204 })),
      "DELETE /api/feeds/3": () => new Response(null, { status: 204 }),
    });
    const ui = (feeds: Feed[]) => (
      <QueryClientProvider client={new QueryClient()}>
        <DeleteDialog feeds={feeds} onClose={() => undefined} onDone={() => undefined} />
      </QueryClientProvider>
    );
    const all = [feed("1"), feed("2"), feed("3")];
    const { rerender } = render(ui(all));
    await userEvent.setup().click(screen.getByRole("button", { name: "Delete 3 feeds" }));
    const bar = await screen.findByRole("progressbar", { name: "Deleting feeds" });
    await waitFor(() => expect(bar).toHaveAttribute("value", "1"));
    // The first feed is gone, so the screen's selection now holds two.
    rerender(ui(all.slice(1)));
    expect(screen.getByRole("dialog", { name: "Delete 3 feeds?" })).toBeInTheDocument();
    expect(bar).toHaveAttribute("max", "3");
    release();
  });
});

describe("deleteCopy", () => {
  it("reads right for one feed", () => {
    const c = deleteCopy(1, 2);
    expect(c.feeds).toBe("feed");
    expect(c.consequence).toBe("Its articles are removed from Kipple. Your sync apps stop seeing this feed.");
    expect(c.starredWarning).toBe("This feed holds 2 starred articles. By default they are kept in the Archive.");
    expect(deleteCopy(1, 1).starredWarning).toBe("This feed holds 1 starred article. By default it is kept in the Archive.");
    expect(c.noStarred).toBe("This feed has no starred articles.");
  });
  it("reads right for several feeds", () => {
    const c = deleteCopy(3, 4);
    expect(c.feeds).toBe("feeds");
    expect(c.consequence).toBe("Their articles are removed from Kipple. Your sync apps stop seeing these feeds.");
    expect(c.starredWarning).toBe("These feeds hold 4 starred articles in total. By default they are kept in the Archive.");
    expect(c.noStarred).toBe("None of these feeds has starred articles.");
  });
  it("shows the single-feed copy in the dialog", () => {
    mockFetch({});
    render(
      <QueryClientProvider client={new QueryClient()}>
        <DeleteDialog feeds={[{ ...feed("1"), starred_count: 2 } as Feed]} onClose={() => undefined} onDone={() => undefined} />
      </QueryClientProvider>,
    );
    expect(screen.getByRole("dialog", { name: "Delete 1 feed?" })).toBeInTheDocument();
    expect(screen.getByText(/Its articles are removed/)).toBeInTheDocument();
    expect(screen.getByText(/This feed holds 2 starred articles\./)).toBeInTheDocument();
  });
});
