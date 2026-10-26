import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError } from "@/api/client";
import type { OpmlResult } from "@/api/admin";
import { initialLive, liveStore } from "@/api/events";
import { keys } from "@/api/queries";
import type { Bootstrap, Feed, Folder } from "@/api/types";
import { bootstrap } from "@/test/mockApi";
import { OpmlResultSummary, opmlError } from "./OpmlDialog";
import { MoveDialog } from "./BulkActions";
import { ApplyProgress } from "@/shell/FilterHost";

// What an import says when it is refused (issues #487, #467), the fetch progress after it (issue #464), and the bulk
// move's starting choice (issue #487).

afterEach(() => {
  vi.unstubAllGlobals();
  liveStore.set(initialLive);
});

const result: OpmlResult = { folders_created: 0, feeds_added: 3, feeds_existing: [], folders_merged_case: [], memberships_dropped: [], run_id: "7" };

describe("an import that is refused", () => {
  it("says the server's reason for an empty file, a file with no feeds and an undefined entity", () => {
    expect(opmlError(new ApiError(400, "empty_opml", { error: "empty_opml", message: "that file is empty" }))).toBe("Kipple can't import that file: that file is empty.");
    expect(opmlError(new ApiError(400, "opml_entity", { error: "opml_entity", message: "&x; on line 1 is not an entity XML or HTML defines" }))).toBe(
      "Kipple can't import that file: &x; on line 1 is not an entity XML or HTML defines.",
    );
    expect(opmlError(new ApiError(400, "not_opml"))).toMatch(/^That is not an OPML file/);
  });
});

describe("the fetch after an import", () => {
  it("shows a bar while the new feeds are fetched, then how it ended", () => {
    render(<OpmlResultSummary result={result} />);
    // Shown from the import's answer, before any event arrives.
    expect(screen.getByText("Fetching the new feeds: 0 of 3")).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "Fetching the new feeds" })).toHaveAttribute("max", "3");

    act(() => liveStore.set((s) => ({ ...s, runs: { "7": { ...s.runs["7"]!, done: 2 } } })));
    expect(screen.getByText("Fetching the new feeds: 2 of 3")).toBeInTheDocument();

    act(() => liveStore.set((s) => ({ ...s, runs: {}, finished: { "7": { kind: "import", errors: 1, seq: 9, at: Date.now() } } })));
    expect(screen.getByText("Kipple fetched the new feeds. 1 couldn't be fetched; each says why on its row in Feeds.")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).toBeNull();
  });
});

describe("the fetch after an import, once the dialog is closed (issue #464)", () => {
  it("stays on screen under the top bar until the run ends", () => {
    const qc = new QueryClient();
    const dialog = render(<OpmlResultSummary result={{ ...result, feeds_added: 500 }} />);
    const shell = render(
      <QueryClientProvider client={qc}>
        <ApplyProgress />
      </QueryClientProvider>,
    );
    // Closing the dialog does not end the run: the shell keeps showing it.
    dialog.unmount();
    const bar = within(shell.container).getByRole("progressbar", { name: "Fetching imported feeds" });
    expect(bar).toHaveAttribute("aria-valuemax", "500");
    expect(bar).toHaveAttribute("aria-valuenow", "0");

    act(() => liveStore.set((s) => ({ ...s, runs: { "7": { ...s.runs["7"]!, done: 120, errors: 4 } } })));
    expect(within(shell.container).getByTestId("import-progress")).toHaveTextContent("Fetching imported feeds: 120 of 500, 4 couldn't be fetched");
    expect(bar).toHaveAttribute("aria-valuenow", "120");

    act(() => liveStore.set((s) => ({ ...s, runs: {}, finished: { "7": { kind: "import", errors: 4, seq: 9, at: Date.now() } } })));
    expect(within(shell.container).queryByTestId("import-progress")).toBeNull();
  });
});

describe("the bulk move", () => {
  it("opens with no folder chosen, so one tap on Move files nothing", () => {
    const folders: Folder[] = [
      { id: "1", name: "Uncategorized", position: 0, is_default: true, unread: 0 },
      { id: "2", name: "News", position: 1, is_default: false, unread: 0 },
    ];
    const qc = new QueryClient();
    qc.setQueryData<Bootstrap>(keys.bootstrap, { ...bootstrap, folders });
    const feeds = [{ ...bootstrap.feeds[0]!, id: "5", folder_id: "1" } as Feed];
    render(
      <QueryClientProvider client={qc}>
        <MoveDialog feeds={feeds} folders={folders} allFeeds={feeds} onClose={() => undefined} onDone={() => undefined} />
      </QueryClientProvider>,
    );
    expect(screen.getByRole("combobox", { name: "Move to folder" })).toHaveValue("");
    expect(screen.getByRole("button", { name: "Move" })).toBeDisabled();
  });
});

describe("a run whose end was never heard", () => {
  const ended = /^The fetch of the new feeds has ended\. A feed that can't be fetched says why/;

  it("still says the fetch is over once the run is gone, without saying it went well", () => {
    render(<OpmlResultSummary result={result} />);
    expect(screen.getByText("Fetching the new feeds: 0 of 3")).toBeInTheDocument();
    // The status poll drops a run the stream never reported finishing.
    act(() => liveStore.set((s) => ({ ...s, runs: {} })));
    expect(screen.getByText(ended)).toBeInTheDocument();
    expect(screen.queryByText(/Kipple fetched the new feeds/)).toBeNull();
    expect(screen.queryByRole("progressbar")).toBeNull();
  });

  // Issue #536 (round 2): a status poll that stops listing the run records an inferred end. It has no failure count
  // to report, so it must not read as the exact "Kipple fetched the new feeds."
  it("words a poll-inferred end as an end, not as a clean result", () => {
    render(<OpmlResultSummary result={result} />);
    act(() => {
      liveStore.set((s) => ({ ...s, runs: {}, finished: { "7": { kind: "import", errors: 0, inferred: true, seq: 9, at: Date.now() } } }));
    });
    expect(screen.getByText(ended)).toBeInTheDocument();
    expect(screen.queryByText(/Kipple fetched the new feeds/)).toBeNull();
  });
});
