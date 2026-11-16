import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { BoneLines, Skeleton } from "@/ui/kit";
import { cards } from "./cards";
import { compact } from "./compact";
import { digest } from "./digest";
import { gazette } from "./gazette";
import { headlines } from "./headlines";
import { inbox } from "./inbox";
import { magazine } from "./magazine";
import { results } from "./results";

describe("row skeletons hold the room of the rows that replace them", () => {
  const draw = (layout: { Skeleton?: React.ComponentType }) => {
    const Sk = layout.Skeleton!;
    return render(<Sk />).container.firstElementChild as HTMLElement;
  };
  const classes = (el: Element | null) => el?.className ?? "";

  it("Editorial: the row's own padding and picture box, then bars for the source, title and excerpt lines cut to the density", () => {
    const row = draw(magazine);
    const frame = row.firstElementChild;
    expect(classes(frame)).toContain("py-[var(--row-py)]");
    expect(classes(frame)).toContain("@[34rem]:py-[calc(var(--row-py)+0.5rem)]");
    // The picture is 2:1 across the row and 16:9 beside the text, as the row's own is.
    const picture = row.querySelector(".aspect-\\[2\\/1\\]");
    expect(classes(picture)).toContain("w-full");
    expect(classes(picture)).toContain("@[34rem]:aspect-video");
    // Title and excerpt bars are as many lines as the density shows, at most two. The star and More sit on the source line, so nothing follows the text.
    expect(row.querySelector('[style*="--title-lines"]')).not.toBeNull();
    expect(row.querySelector('[style*="--snippet-lines"]')).not.toBeNull();
    expect(row.children).toHaveLength(1);
  });

  it("Cards: the card shell, the picture's frame (2:1 on a phone, 16:9 in a grid) and bars for the text lines", () => {
    const row = draw(cards);
    expect(classes(row)).toContain("rounded-2xl");
    expect(classes(row.querySelector(".aspect-video"))).toContain("max-sm:aspect-[2/1]");
    expect(row.querySelector('[style*="--title-lines"]')).not.toBeNull();
    expect(row.children).toHaveLength(2);
  });

  it("Inbox and search results: two title lines, a source line and the snippet", () => {
    const inboxRow = draw(inbox);
    expect(inboxRow.querySelectorAll('[class*="h-[1lh]"]').length).toBe(2 + 1 + 2);
    const resultRow = draw(results);
    expect(resultRow.querySelectorAll('[class*="h-[1lh]"]').length).toBe(2 + 1 + 1);
  });

  it("text bars are a whole line high in the type around them, cut to the lines a variable allows", () => {
    const { container } = render(<BoneLines lines={3} max="--snippet-lines" className="text-sm" />);
    const box = container.firstElementChild as HTMLElement;
    expect(box.style.maxHeight).toBe("calc(var(--snippet-lines) * 1lh)");
    expect(box.querySelectorAll('[class*="h-[1lh]"]')).toHaveLength(3);
    expect(box.lastElementChild?.firstElementChild?.className).toContain("w-2/3");
    // Bars past the limit are cut off, not drawn outside the box.
    expect(box.className).toContain("overflow-hidden");
  });

  it("Cards: the excerpt is three lines at every density, as CardRow clamps it, not the density's snippet lines", () => {
    const row = draw(cards);
    expect(row.querySelector('[style*="--snippet-lines"]')).toBeNull();
    const bars = [...row.querySelectorAll('[class*="h-[1lh]"]')];
    expect(bars.length).toBe(1 + 2 + 3);
  });
});

describe("layout row skeletons", () => {
  for (const layout of [magazine, cards, compact, inbox, headlines, gazette, digest, results]) {
    it(`${layout.id} exports one and the kit Skeleton draws it once per row`, () => {
      expect(layout.Skeleton).toBeTypeOf("function");
      const { container } = render(<Skeleton rows={3} row={layout.Skeleton} />);
      const root = container.querySelector("[data-skeleton]")!;
      // The live label plus one element per row.
      expect(root.children).toHaveLength(4);
      expect(root.querySelector(".animate-pulse")).not.toBeNull();
    });
  }
});
