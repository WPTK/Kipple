import { describe, expect, it } from "vitest";
import { hasNothingToRead } from "./articleDom";

describe("hasNothingToRead", () => {
  it.each(["", "<p> </p>", "<p>&nbsp;</p>", "<p>&#160;</p><div>\n</div>", "<p><br></p>", "<!-- note --><span></span>"])("is true for %j", (html) => {
    expect(hasNothingToRead(html)).toBe(true);
  });

  it.each(["<p>Hello</p>", '<p><img src="/x" alt=""></p>', "<pre> </pre>", "<table></table>", "<figure></figure>"])("is false for %j", (html) => {
    expect(hasNothingToRead(html)).toBe(false);
  });

  it("is false for a long body, whatever it holds", () => {
    expect(hasNothingToRead(`<p>${" ".repeat(4000)}</p>`)).toBe(false);
  });
});
