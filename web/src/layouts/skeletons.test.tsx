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
    expect(classes(row)).toContain("py-[calc(var(--row-py)+0.5rem)]");
    expect(classes(row.querySelector(".aspect-video"))).toContain("w-full");
    // Title and excerpt bars are as many lines as the density shows, at most two; the buttons' height is kept below the text.
    expect(row.querySelector('[style*="--title-lines"]')).not.toBeNull();
    expect(row.querySelector('[style*="--snippet-lines"]')).not.toBeNull();
    expect(classes(row.lastElementChild)).toContain("h-11");
  });

  it("Cards: the card shell, the picture's frame, bars for the text lines and the footer's height", () => {
    const row = draw(cards);
    expect(classes(row)).toContain("rounded-2xl");
    expect(row.querySelector(".aspect-video")).not.toBeNull();
    expect(row.querySelector('[style*="--title-lines"]')).not.toBeNull();
    expect(classes(row.lastElementChild)).toContain("h-12");
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
