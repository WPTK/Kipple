import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, errorMessage, failed, failureCause } from "./client";
import { setBrowserOnline } from "@/lib/offlineState";
import { act, render, screen } from "@testing-library/react";
import { Toasts, clearToasts, settleToasts, toast } from "@/shell/toasts";

function online(on: boolean) {
  setBrowserOnline(on);
}

/** An error answer the way Kipple sends it (it carries Kipple's API header), or as a proxy in front of it would not. */
function answer(status: number, fromKipple: boolean) {
  const e = new ApiError(status, "http_" + status);
  e.fromKipple = fromKipple;
  return e;
}

describe("failure wording by cause", () => {
  afterEach(() => {
    setBrowserOnline(true);
    vi.restoreAllMocks();
    clearToasts();
  });

  it("tells no network, no answer and an error answer apart", () => {
    online(false);
    expect(errorMessage(new ApiError(0, "network"))).toBe("You're offline.");
    online(true);
    expect(errorMessage(new ApiError(0, "network"))).toBe("Kipple can't be reached.");
    expect(errorMessage(answer(503, true))).toBe("Kipple's server had a problem.");
    expect(failureCause(new ApiError(400, "bad"))).toBeNull();
  });

  it("calls an error answer a proxy or tunnel sent for a Kipple that is down 'can't be reached', not a server problem", () => {
    online(true);
    for (const status of [502, 503, 504, 530]) expect(failureCause(answer(status, false))).toBe("unreachable");
    expect(failureCause(answer(500, true))).toBe("server");
    expect(failureCause(new ApiError(503, "maintenance"))).toBeNull();
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
