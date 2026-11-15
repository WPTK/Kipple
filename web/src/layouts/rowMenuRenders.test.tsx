import { Profiler } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { closeRowMenu, openRowMenu } from "@/gestures/rowMenu";
import { card } from "@/test/mockApi";
import { RowMenu } from "./parts";
import type { RowMenuActions } from "./types";

// A long page (the Gazette, the Digest) draws every story's menu, so opening or closing one menu must leave the others alone.

afterEach(() => {
  act(() => closeRowMenu());
  document.body.replaceChildren();
});

describe("row menus drawn together", () => {
  it("opening and closing one menu draws that menu again and no other", async () => {
    const actions = new Proxy({}, { get: () => vi.fn() }) as unknown as RowMenuActions;
    const drawn = { a: 0, b: 0, c: 0 };
    const count = (id: keyof typeof drawn) => () => void (drawn[id] += 1);
    render(
      <>
        <Profiler id="a" onRender={count("a")}>
          <RowMenu item={card(1)} actions={actions} />
        </Profiler>
        <Profiler id="b" onRender={count("b")}>
          <RowMenu item={card(2)} actions={actions} />
        </Profiler>
        <Profiler id="c" onRender={count("c")}>
          <RowMenu item={card(3)} actions={actions} />
        </Profiler>
      </>,
    );
    const before = { ...drawn };
    act(() => openRowMenu(card(2).id));
    expect(await screen.findByRole("menu")).toBeInTheDocument();
    expect(drawn.a).toBe(before.a);
    expect(drawn.c).toBe(before.c);
    expect(drawn.b).toBeGreaterThan(before.b);

    const open = { ...drawn };
    act(() => closeRowMenu());
    expect(screen.queryByRole("menu")).toBeNull();
    expect(drawn.a).toBe(open.a);
    expect(drawn.c).toBe(open.c);
    expect(drawn.b).toBeGreaterThan(open.b);
  });

  it("moving the open menu from one story to another draws only those two", () => {
    const actions = new Proxy({}, { get: () => vi.fn() }) as unknown as RowMenuActions;
    const drawn = { a: 0, b: 0, c: 0 };
    const count = (id: keyof typeof drawn) => () => void (drawn[id] += 1);
    render(
      <>
        <Profiler id="a" onRender={count("a")}>
          <RowMenu item={card(1)} actions={actions} />
        </Profiler>
        <Profiler id="b" onRender={count("b")}>
          <RowMenu item={card(2)} actions={actions} />
        </Profiler>
        <Profiler id="c" onRender={count("c")}>
          <RowMenu item={card(3)} actions={actions} />
        </Profiler>
      </>,
    );
    act(() => openRowMenu(card(1).id));
    const first = { ...drawn };
    act(() => openRowMenu(card(2).id));
    expect(drawn.c).toBe(first.c);
    expect(drawn.a).toBeGreaterThan(first.a);
    expect(drawn.b).toBeGreaterThan(first.b);
  });
});
