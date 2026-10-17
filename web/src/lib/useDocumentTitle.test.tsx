import { render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { documentTitle, useDocumentTitle } from "./useDocumentTitle";

function Screen({ heading }: { heading?: string }) {
  useDocumentTitle(heading);
  return null;
}

afterEach(() => {
  document.title = "Kipple";
});

describe("documentTitle", () => {
  it("joins the heading and the app name", () => {
    expect(documentTitle("Settings")).toBe("Settings - Kipple");
  });
  it("is just the app name without a heading", () => {
    expect(documentTitle()).toBe("Kipple");
    expect(documentTitle("  ")).toBe("Kipple");
  });
  it("does not repeat the app name when the heading has it", () => {
    expect(documentTitle("Sign in to Kipple")).toBe("Sign in to Kipple");
  });
});

describe("useDocumentTitle", () => {
  it("sets the title, follows the heading and restores it on unmount", () => {
    const { rerender, unmount } = render(<Screen heading="Unread" />);
    expect(document.title).toBe("Unread - Kipple");
    rerender(<Screen heading="Starred" />);
    expect(document.title).toBe("Starred - Kipple");
    unmount();
    expect(document.title).toBe("Kipple");
  });
  it("leaves the title alone while the heading is not known yet", () => {
    document.title = "Kipple";
    render(<Screen />);
    expect(document.title).toBe("Kipple");
  });
  it("a screen replacing another takes over the title", () => {
    const a = render(<Screen heading="Feeds" />);
    a.unmount();
    render(<Screen heading="Search" />);
    expect(document.title).toBe("Search - Kipple");
  });
  it("one screen unmounting while another mounts in the same commit leaves the new title", () => {
    // The old screen's cleanup runs before the new one's effect, so the reset to "Kipple" never wins.
    const { rerender } = render(<Screen key="a" heading="Feeds" />);
    expect(document.title).toBe("Feeds - Kipple");
    rerender(<Screen key="b" heading="Search" />);
    expect(document.title).toBe("Search - Kipple");
  });
  it("a screen replaced by one with no heading yet falls back to the app name", () => {
    const { rerender } = render(<Screen key="a" heading="Feeds" />);
    rerender(<Screen key="b" />);
    expect(document.title).toBe("Kipple");
  });
});
