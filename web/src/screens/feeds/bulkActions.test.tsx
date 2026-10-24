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
  it("keeps its total fixed while the live selection changes, and deletes the whole set in one call", async () => {
    let release: () => void = () => undefined;
    const held = new Promise<void>((r) => (release = r));
    const { calls } = mockFetch({
      "POST /api/feeds/delete": async () => (await held, new Response(JSON.stringify({ deleted: 3 }), { status: 200, headers: { "Content-Type": "application/json" } })),
    });
    const ui = (feeds: Feed[]) => (
      <QueryClientProvider client={new QueryClient()}>
        <DeleteDialog feeds={feeds} onClose={() => undefined} onDone={() => undefined} />
      </QueryClientProvider>
    );
    const all = [feed("1"), feed("2"), feed("3")];
    const { rerender } = render(ui(all));
    await userEvent.setup().click(screen.getByRole("button", { name: "Delete 3 feeds" }));
    await screen.findByRole("progressbar", { name: "Deleting feeds" });
    expect(screen.getByText(/Deleting 3 feeds\. This keeps going on the server if you close this page/)).toBeInTheDocument();
    // The screen's selection changes underneath; the dialog still deletes what it showed.
    rerender(ui(all.slice(1)));
    expect(screen.getByRole("dialog", { name: "Delete 3 feeds?" })).toBeInTheDocument();
    release();
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/feeds/delete")).toHaveLength(1));
    expect(JSON.parse(String(calls.find((c) => c.url.pathname === "/api/feeds/delete")!.init?.body))).toEqual({ ids: ["1", "2", "3"], delete_starred: false });
  });
});

describe("deleteCopy", () => {
  it("reads right for one feed", () => {
    const c = deleteCopy(1, 2);
    expect(c.feeds).toBe("feed");
    expect(c.consequence).toBe("Removes its articles. This can't be undone.");
    expect(c.button).toBe("Delete feed");
    expect(c.starredWarning).toBe("This feed holds 2 starred articles. By default they are kept in the Archive.");
    expect(deleteCopy(1, 1).starredWarning).toBe("This feed holds 1 starred article. By default it is kept in the Archive.");
    expect(c.noStarred).toBe("This feed has no starred articles.");
  });
  it("reads right for several feeds", () => {
    const c = deleteCopy(3, 4);
    expect(c.feeds).toBe("feeds");
    expect(c.consequence).toBe("Removes their articles. This can't be undone.");
    expect(c.button).toBe("Delete 3 feeds");
    expect(c.starredWarning).toBe("These feeds hold 4 starred articles in total. By default they are kept in the Archive.");
    expect(c.noStarred).toBe("None of these feeds has starred articles.");
  });
  it("states what goes and names the outcome once the articles are counted", () => {
    expect(deleteCopy(1, 2, 312).consequence).toBe("Removes 310 articles. This can't be undone.");
    expect(deleteCopy(1, 2, 312).button).toBe("Delete feed and 310 articles");
    // Ticking "Delete starred articles too" adds the starred ones to what goes.
    expect(deleteCopy(1, 2, 312, true).button).toBe("Delete feed and 312 articles");
    expect(deleteCopy(3, 0, 1).button).toBe("Delete 3 feeds and 1 article");
    expect(deleteCopy(1, 0, 0)).toMatchObject({ consequence: "Removes no articles. This can't be undone.", button: "Delete feed" });
  });
  it("shows the single-feed copy in the dialog", () => {
    mockFetch({});
    render(
      <QueryClientProvider client={new QueryClient()}>
        <DeleteDialog feeds={[{ ...feed("1"), starred_count: 2 } as Feed]} onClose={() => undefined} onDone={() => undefined} />
      </QueryClientProvider>,
    );
    expect(screen.getByRole("dialog", { name: "Delete 1 feed?" })).toBeInTheDocument();
    expect(screen.getByText(/Removes its articles\. This can't be undone\./)).toBeInTheDocument();
    expect(screen.getByText(/This feed holds 2 starred articles\./)).toBeInTheDocument();
  });
});
