import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Skeleton } from "@/ui/kit";
import { cards } from "./cards";
import { compact } from "./compact";
import { digest } from "./digest";
import { gazette } from "./gazette";
import { headlines } from "./headlines";
import { inbox } from "./inbox";
import { magazine } from "./magazine";
import { results } from "./results";

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
