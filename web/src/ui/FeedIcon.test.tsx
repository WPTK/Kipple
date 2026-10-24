import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import schemes from "@/theme/schemes.json";
import { FeedIcon, TILES } from "./FeedIcon";

describe("FeedIcon", () => {
  it("never paints a tile in a status color, in any scheme", () => {
    for (const s of schemes) {
      const t = s.tokens as Record<string, string>;
      for (const tile of TILES) {
        for (const status of ["unread", "accent", "star", "danger"]) {
          expect(t[tile.token]!.toLowerCase(), `${s.id}: ${tile.token} equals ${status}`).not.toBe(t[status]!.toLowerCase());
        }
      }
    }
  });

  it("keeps the letter out of the text content, so a row's visible text is only its name", () => {
    const { container } = render(<FeedIcon src={null} title="Go Blog" />);
    const tile = container.firstElementChild as HTMLElement;
    expect(tile.textContent).toBe("");
    expect(tile.dataset.letter).toBe("G");
  });
});
