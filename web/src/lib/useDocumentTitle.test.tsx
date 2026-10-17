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
});
