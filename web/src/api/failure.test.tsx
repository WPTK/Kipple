import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, errorMessage, failed, failureCause } from "./client";
import { act, render, screen } from "@testing-library/react";
import { Toasts, clearToasts, settleToasts, toast } from "@/shell/toasts";

function online(on: boolean) {
  vi.spyOn(navigator, "onLine", "get").mockReturnValue(on);
}

describe("failure wording by cause", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    clearToasts();
  });

  it("tells no network, no answer and an error answer apart", () => {
    online(false);
    expect(errorMessage(new ApiError(0, "network"))).toBe("You're offline.");
    online(true);
    expect(errorMessage(new ApiError(0, "network"))).toBe("Kipple can't be reached.");
    expect(errorMessage(new ApiError(503, "http_503"))).toBe("Kipple's server had a problem.");
    expect(failureCause(new ApiError(400, "bad"))).toBeNull();
  });

  it("names the action, then the cause, and never ends on a retry word", () => {
    online(true);
    expect(failed("star this article", new ApiError(0, "network"))).toBe("Couldn't star this article. Kipple can't be reached.");
    expect(failed("save the search", new Error("x"))).toBe("Couldn't save the search. Something went wrong.");
  });

  it("clears an error toast when a later change on the same topic goes through", () => {
    render(<Toasts />);
    act(() => {
      toast("Couldn't star this article. Kipple can't be reached.", "error", undefined, "star");
      toast("Link copied");
    });
    expect(screen.getByText(/Couldn't star/)).toBeInTheDocument();
    act(() => settleToasts("star"));
    expect(screen.queryByText(/Couldn't star/)).toBeNull();
    expect(screen.getByText("Link copied")).toBeInTheDocument();
  });

  it("shows one toast per kind: a newer error replaces the older one", () => {
    render(<Toasts />);
    act(() => {
      toast("Couldn't star this article. Kipple can't be reached.", "error", undefined, "star");
      toast("Couldn't mark as read. Kipple can't be reached.", "error", undefined, "read");
      toast("Link copied");
    });
    expect(screen.queryByText(/Couldn't star/)).toBeNull();
    expect(screen.getByText(/Couldn't mark as read/)).toBeInTheDocument();
    expect(screen.getByText("Link copied")).toBeInTheDocument();
  });
});
