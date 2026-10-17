import { describe, expect, it } from "vitest";
import { fireEvent, render } from "@testing-library/react";
import { ListImage, useImageFailure } from "./parts";

function Pic({ src, frame }: { src: string; frame?: { className?: string } }) {
  const failure = useImageFailure(src);
  return (
    <>
      <ListImage src={src} failure={failure} frame={frame} className="pic" />
      <span data-testid="state">{failure.failed ? "failed" : "ok"}</span>
    </>
  );
}

describe("ListImage", () => {
  it("removes the picture and its frame together when it cannot be shown", () => {
    const { container, getByTestId } = render(<Pic src="/a.bmp" frame={{ className: "frame" }} />);
    expect(container.querySelector(".frame .pic")).toBeTruthy();
    fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector(".frame, img")).toBeNull();
    expect(getByTestId("state").textContent).toBe("failed");
  });

  it("removes a bare picture", () => {
    const { container } = render(<Pic src="/a.bmp" />);
    fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).toBeNull();
  });

  it("tries a new image again", () => {
    const { container, rerender, getByTestId } = render(<Pic src="/a.bmp" />);
    fireEvent.error(container.querySelector("img")!);
    rerender(<Pic src="/b.jpg" />);
    expect(container.querySelector("img")?.getAttribute("src")).toBe("/b.jpg");
    expect(getByTestId("state").textContent).toBe("ok");
  });
});
