import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { OpmlResult } from "@/api/admin";
import { json, mockFetch } from "@/test/mockApi";
import { OpmlImportDialog, OpmlResultSummary } from "./OpmlDialog";

afterEach(() => vi.unstubAllGlobals());

const base: OpmlResult = { folders_created: 0, feeds_added: 0, feeds_existing: [], folders_merged_case: [], memberships_dropped: [] };

describe("the OPML import report", () => {
  it("says which existing feeds moved, which folders were left empty, and counts only the rest as left alone", () => {
    render(
      <OpmlResultSummary
        result={{
          ...base,
          feeds_existing: [
            { url: "https://a.example/feed", feed_id: "1" },
            { url: "https://b.example/feed", feed_id: "2" },
            { url: "https://c.example/feed", feed_id: "3" },
          ],
          feeds_moved: [
            { url: "https://a.example/feed", feed_id: "1" },
            { url: "https://b.example/feed", feed_id: "2" },
          ],
          folders_emptied: ["Old", "Tech/Old"],
        }}
      />,
    );
    expect(screen.getByText("2 feeds you already had were moved into the file's folders")).toBeInTheDocument();
    expect(screen.getByText("1 feed was already in Kipple and was left as it is")).toBeInTheDocument();
    expect(screen.getByText("Folders now empty, kept: Old, Tech/Old")).toBeInTheDocument();
  });

  it("lists folders that could not be made, with the reason, and where their feeds went", () => {
    render(<OpmlResultSummary result={{ ...base, folders_refused: [{ path: "L1/L2/L3/L4/L5/L6/L7/L8/L9", reason: "folders nest at most 8 levels deep" }] }} />);
    expect(screen.getByText("1 folder was not created")).toBeInTheDocument();
    expect(screen.getByText("L1/L2/L3/L4/L5/L6/L7/L8/L9: folders nest at most 8 levels deep")).toBeInTheDocument();
    expect(screen.getByText(/nearest folder above/)).toBeInTheDocument();
  });

  it("says where the feeds of a folder with a matching full path went, names joined with ›", () => {
    render(<OpmlResultSummary result={{ ...base, folders_merged_path: [{ kept: ["Music", "AC/DC"], merged: ["Music", "AC", "DC"] }] }} />);
    expect(screen.getByText("The feeds of Music › AC › DC were filed into Music › AC/DC")).toBeInTheDocument();
  });

  it("shows none of it for a plain import (the fields absent or empty)", () => {
    render(<OpmlResultSummary result={{ ...base, feeds_added: 3, feeds_moved: [], folders_emptied: [], folders_refused: [] }} />);
    expect(screen.queryByText(/moved into/)).toBeNull();
    expect(screen.queryByText(/not created/)).toBeNull();
    expect(screen.queryByText(/now empty/)).toBeNull();
  });

  it("the move switch is off by default and, turned on, asks the server to move existing feeds", async () => {
    const { calls } = mockFetch({ "POST /api/opml": () => json({ ...base, feeds_moved: [{ url: "https://a.example/feed", feed_id: "1" }], feeds_existing: [{ url: "https://a.example/feed", feed_id: "1" }] }) });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <OpmlImportDialog onClose={() => undefined} />
      </QueryClientProvider>,
    );
    const user = userEvent.setup();
    const sw = screen.getByRole("switch", { name: /^Move feeds that already exist into the file's folders/ });
    expect(sw).not.toBeChecked();
    await user.upload(screen.getByLabelText("OPML file"), new File(["<opml/>"], "subs.opml", { type: "text/xml" }));
    await user.click(sw);
    await waitFor(() => expect(screen.getByRole("button", { name: "Import" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "Import" }));
    expect(await screen.findByText("1 feed you already had was moved into the file's folder")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.url.searchParams.get("move_existing")).toBe("true");
  });
});
