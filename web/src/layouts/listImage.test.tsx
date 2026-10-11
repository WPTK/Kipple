import { describe, expect, it } from "vitest";
import { fireEvent, render } from "@testing-library/react";
import { ListImage } from "./parts";

describe("ListImage", () => {
  it("removes the picture and its frame together when it cannot be shown", () => {
    const { container } = render(<ListImage src="/a.bmp" frame={{ className: "frame" }} className="pic" />);
    expect(container.querySelector(".frame .pic")).toBeTruthy();
    fireEvent.error(container.querySelector("img")!);
    expect(container.innerHTML).toBe("");
  });

  it("removes a bare picture", () => {
    const { container } = render(<ListImage src="/a.bmp" className="pic" />);
    fireEvent.error(container.querySelector("img")!);
    expect(container.innerHTML).toBe("");
  });

  it("tries a new image again", () => {
    const { container, rerender } = render(<ListImage src="/a.bmp" className="pic" />);
    fireEvent.error(container.querySelector("img")!);
    rerender(<ListImage src="/b.jpg" className="pic" />);
    expect(container.querySelector("img")?.getAttribute("src")).toBe("/b.jpg");
  });
});
