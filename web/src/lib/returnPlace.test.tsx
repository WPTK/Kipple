import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { createMemoryRouter, RouterProvider, useNavigate } from "react-router";
import { useReturnPlace } from "./returnPlace";

// The place of a page made of blocks belongs to the history entry it was left in.

let rowY = 500;
function Page() {
  const { scroller, stop, setStop } = useReturnPlace<HTMLDivElement>("blocks", true);
  const navigate = useNavigate();
  return (
    <div>
      <div ref={scroller} data-testid="scroller">
        <div>
          <div data-stop-id="row" data-testid="row">
            <a href="#row" onFocus={() => setStop("row")}>
              row
            </a>
          </div>
        </div>
      </div>
      <output data-testid="stop">{stop ?? ""}</output>
      <button onClick={() => void navigate("/other")}>open</button>
      <button onClick={() => void navigate("/blocks")}>again</button>
    </div>
  );
}

function Other() {
  const navigate = useNavigate();
  return <button onClick={() => void navigate("/blocks")}>again</button>;
}

function setup() {
  const router = createMemoryRouter(
    [
      { path: "/blocks", element: <Page /> },
      { path: "/other", element: <Other /> },
    ],
    { initialEntries: ["/blocks"] },
  );
  render(<RouterProvider router={router} />);
  return router;
}
const scroller = () => screen.getByTestId("scroller");
const scrollTo = (n: number) => {
  scroller().scrollTop = n;
  act(() => void scroller().dispatchEvent(new Event("scroll")));
};
const press = (name: string) => act(() => screen.getByRole("button", { name }).click());
const back = async (router: ReturnType<typeof setup>) => {
  await act(async () => {
    await router.navigate(-1);
  });
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  rowY = 500;
});

describe("useReturnPlace", () => {
  it("gives each history entry of the page its own place when Back is pressed several times", async () => {
    const router = setup();
    scrollTo(100);
    press("open"); // entries: blocks (left at 100), other
    press("again"); // blocks (100), other, blocks (new)
    scrollTo(500);
    press("open"); // blocks (100), other, blocks (500), other
    await back(router);
    expect(scroller().scrollTop).toBe(500);
    await back(router);
    await back(router); // the first entry again, left at 100 (not the newest place)
    expect(scroller().scrollTop).toBe(100);
  });

  it("keeps the remembered row where it was left when the rows above it change while the page settles", async () => {
    let settle: () => void = () => {};
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(cb: () => void) {
          settle = cb;
        }
        observe() {}
        disconnect() {}
        unobserve() {}
      },
    );
    const rect = (top: number) => ({ top, bottom: top, left: 0, right: 0, width: 0, height: 0, x: 0, y: top, toJSON: () => ({}) }) as DOMRect;
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      return this.dataset.testid === "row" ? rect(rowY - scroller().scrollTop) : rect(0);
    });
    const router = setup();
    scrollTo(200); // the row is 300 below the top of the box
    act(() => screen.getByRole("link", { name: "row" }).focus());
    press("open");
    rowY = 600; // the data was fetched again and the rows above grew
    await back(router);
    expect(scroller().scrollTop).toBe(300);
    rowY = 650;
    act(() => settle());
    expect(scroller().scrollTop).toBe(350);
    expect(screen.getByTestId("stop")).toHaveTextContent("row");
  });
});
