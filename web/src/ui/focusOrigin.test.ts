import { afterEach, describe, expect, it } from "vitest";
import { focusOrigin } from "./kit";

afterEach(() => {
  document.body.innerHTML = "";
});

function menu(noReturn: boolean): { trigger: HTMLElement; item: HTMLElement } {
  document.body.innerHTML = `<button aria-controls="m1">More</button><div role="menu" id="m1"${noReturn ? " data-no-focus-return" : ""}><div role="menuitem" tabindex="-1">Mute</div></div>`;
  return { trigger: document.querySelector("button") as HTMLElement, item: document.querySelector('[role="menuitem"]') as HTMLElement };
}

describe("focusOrigin", () => {
  it("is null for nothing and for the page", () => {
    expect(focusOrigin(null)).toBeNull();
    expect(focusOrigin(document.body)).toBeNull();
  });

  it("maps a menu item to the button that opened the menu", () => {
    const { trigger, item } = menu(false);
    expect(focusOrigin(item)).toBe(trigger);
  });

  // A row menu opened by a long press gives focus back to nothing, so neither does a dialog opened from it.
  it("is null in a menu that gives focus back to nothing", () => {
    expect(focusOrigin(menu(true).item)).toBeNull();
  });
});
