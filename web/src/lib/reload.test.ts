import { describe, expect, it, vi } from "vitest";
import { reloadToSignIn, SIGN_IN_RELOAD, stripSignInReload } from "./reload";

describe("reload to sign in again", () => {
  it("navigates to the same address with the marker the service worker lets through to the network", () => {
    const assign = vi.fn();
    reloadToSignIn({ href: "https://kipple.test/i/5?from=unread#top", assign }, 1234);
    expect(assign).toHaveBeenCalledWith(`https://kipple.test/i/5?from=unread&${SIGN_IN_RELOAD}=1234#top`);
  });

  it("the marker is taken back out of the address at startup, and nothing else changes", () => {
    const hist = { state: { idx: 3 }, replaceState: vi.fn() };
    stripSignInReload({ href: `https://kipple.test/i/5?from=unread&${SIGN_IN_RELOAD}=1234#top` }, hist);
    expect(hist.replaceState).toHaveBeenCalledWith({ idx: 3 }, "", "/i/5?from=unread#top");
    hist.replaceState.mockClear();
    stripSignInReload({ href: "https://kipple.test/l/unread" }, hist);
    expect(hist.replaceState).not.toHaveBeenCalled();
  });
});
