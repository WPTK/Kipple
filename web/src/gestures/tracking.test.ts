import { afterEach, describe, expect, it, vi } from "vitest";
import { BackSwipe, LONG_PRESS_MS, LongPress, PULL, PullTracker, ROW, RowSwipe, armDistance, rubberBand } from "./tracking";

const W = 375;
const start = (t: RowSwipe, x = 100, y = 100, base = 0) => t.start(x, y, 0, { width: W, viewportWidth: W, base });

/** Drag horizontally from x0 by `dx` in `ms`, in 8 steps, with a little vertical wobble. */
function drag(t: RowSwipe, dx: number, ms: number, x0 = 100, y0 = 100) {
  for (let i = 1; i <= 8; i++) t.move(x0 + (dx * i) / 8, y0 + (i % 2), (ms * i) / 8);
}

describe("row swipe", () => {
  it("arms at 40% of the row, clamped to 96..176 px (150 px at 375)", () => {
    expect(armDistance(375)).toBe(150);
    expect(armDistance(200)).toBe(96);
    expect(armDistance(800)).toBe(176);
  });

  it("locks horizontal only when clearly sideways, else releases to vertical scroll", () => {
    const t = new RowSwipe();
    start(t);
    expect(t.move(104, 100, 10)).toBe("pending"); // under 8 px
    expect(t.move(112, 103, 20)).toBe("horizontal"); // 12 > 1.5 * 3
    const v = new RowSwipe();
    start(v);
    expect(v.move(106, 120, 20)).toBe("vertical");
    expect(v.move(200, 120, 30)).toBe("vertical"); // stays released for the touch
  });

  it("ignores touches starting in the left 24 px (iOS back gesture) and the right 8 px", () => {
    const t = new RowSwipe();
    start(t, 10);
    expect(t.phase).toBe("ignored");
    expect(t.move(200, 100, 50)).toBe("ignored");
    start(t, 370);
    expect(t.phase).toBe("ignored");
    start(t, 24);
    expect(t.phase).toBe("pending");
  });

  it("ignores a second finger", () => {
    const t = new RowSwipe();
    t.start(100, 100, 0, { width: W, viewportWidth: W, multiTouch: true });
    expect(t.phase).toBe("ignored");
  });

  it("commits a slow drag past the arm point (leading)", () => {
    const t = new RowSwipe();
    start(t);
    drag(t, 160, 800);
    expect(t.armed()).toBe(true);
    expect(t.end()).toEqual({ action: "commit", side: "leading" });
  });

  it("cancels a slow drag short of the arm point", () => {
    const t = new RowSwipe();
    start(t);
    drag(t, 100, 1000);
    expect(t.armed()).toBe(false);
    expect(t.end()).toEqual({ action: "close" });
  });

  it("commits a flick: 0.6 px/ms and at least 64 px, even short of the arm point", () => {
    const t = new RowSwipe();
    start(t);
    drag(t, 90, 100); // 0.9 px/ms
    expect(t.end()).toEqual({ action: "commit", side: "leading" });
    const slow = new RowSwipe();
    start(slow);
    drag(slow, 50, 40); // fast but only 50 px
    expect(slow.end().action).toBe("close");
  });

  it("cancels when released moving back toward the origin, even when armed", () => {
    const u = new RowSwipe();
    start(u);
    drag(u, 250, 400);
    u.move(100 + 200, 101, 410);
    u.move(100 + 150, 101, 420);
    expect(u.armed()).toBe(true); // still at the arm point...
    expect(u.end()).toEqual({ action: "close" }); // ...but moving back fast, so it cancels
  });

  it("trailing: a partial swipe past 60 px rests open on Star and More, a full swipe commits", () => {
    const t = new RowSwipe();
    start(t, 300);
    drag(t, -80, 900, 300);
    expect(t.end()).toEqual({ action: "open" });
    const small = new RowSwipe();
    start(small, 300);
    drag(small, -40, 900, 300);
    expect(small.end()).toEqual({ action: "close" });
    const full = new RowSwipe();
    start(full, 300);
    drag(full, -170, 900, 300);
    expect(full.end()).toEqual({ action: "commit", side: "trailing" });
  });

  it("an open trailing row closes when dragged back", () => {
    const t = new RowSwipe();
    start(t, 200, 100, -ROW.revealWidth);
    drag(t, 100, 900, 200);
    expect(t.end()).toEqual({ action: "close" });
  });

  it("rubber-bands past 60% of the row", () => {
    expect(rubberBand(200, W)).toBe(200);
    expect(rubberBand(325, W)).toBe(225 + 50);
    expect(rubberBand(-325, W)).toBe(-(225 + 50));
  });
});

describe("back swipe", () => {
  const go = (x0: number, dx: number, ms: number, dy = 0, blocked = false) => {
    const t = new BackSwipe();
    t.start(x0, 300, 0, W, blocked);
    for (let i = 1; i <= 8; i++) t.move(x0 + (dx * i) / 8, 300 + (dy * i) / 8, (ms * i) / 8);
    return t.end();
  };

  it("pops at 35% of the width, or on a 0.5 px/ms flick of at least 64 px", () => {
    expect(go(40, 140, 1000)).toBe(true); // 37% of 375
    expect(go(40, 100, 1000)).toBe(false);
    expect(go(40, 80, 100)).toBe(true);
    expect(go(40, 50, 40)).toBe(false); // fast but under 64 px
  });

  it("needs a start at least 24 px from the left edge", () => {
    expect(go(23, 200, 500)).toBe(false);
    expect(go(24, 200, 500)).toBe(true);
  });

  it("is rightward only and near-horizontal (2:1)", () => {
    expect(go(100, -200, 500)).toBe(false);
    expect(go(100, 200, 500, 150)).toBe(false); // too diagonal
    expect(go(100, 200, 500, 60)).toBe(true);
  });

  it("does nothing when the DOM says the target is scrollable content", () => {
    expect(go(100, 250, 300, 0, true)).toBe(false);
  });

  it("a release moving leftward does not pop", () => {
    const t = new BackSwipe();
    t.start(100, 300, 0, W, false);
    t.move(200, 300, 100);
    t.move(300, 300, 200);
    t.move(250, 300, 260);
    t.move(180, 300, 300);
    expect(t.end()).toBe(false);
  });
});

describe("pull tracker", () => {
  it("only pulls from scrollTop 0 and downward", () => {
    const p = new PullTracker();
    p.start(100, 100, 40);
    expect(p.move(100, 250, 40)).toBe("dead");
    p.start(100, 100, 0);
    expect(p.move(100, 60, 0)).toBe("dead"); // upward
  });

  it("resists 0.5x, shows at 16 px, arms at 72 px, caps at 120 px", () => {
    const p = new PullTracker();
    p.start(100, 100, 0);
    p.move(100, 120, 0); // 10 px shown
    expect(p.visible()).toBe(false);
    p.move(100, 140, 0); // 20 px
    expect(p.visible()).toBe(true);
    expect(p.armed()).toBe(false);
    p.move(100, 100 + PULL.armAt * 2, 0);
    expect(p.armed()).toBe(true);
    p.move(100, 900, 0);
    expect(p.distance).toBe(PULL.max);
    expect(p.end()).toBe(true);
  });

  it("releasing short of 72 px does not refresh", () => {
    const p = new PullTracker();
    p.start(100, 100, 0);
    p.move(100, 200, 0); // 50 px
    expect(p.end()).toBe(false);
  });

  it("dies when the list scrolls or a row swipe took the touch", () => {
    const p = new PullTracker();
    p.start(100, 100, 0);
    p.move(100, 200, 0);
    expect(p.move(100, 260, 5)).toBe("dead");
    const q = new PullTracker();
    q.start(100, 100, 0);
    expect(q.move(100, 200, 0, true)).toBe("dead");
  });

  it("blocks native scroll only once the pull passes 12 px", () => {
    const p = new PullTracker();
    p.start(100, 100, 0);
    p.move(100, 108, 0);
    expect(p.shouldPreventDefault()).toBe(false);
    p.move(100, 130, 0);
    expect(p.shouldPreventDefault()).toBe(true);
  });
});

describe("long press", () => {
  afterEach(() => vi.useRealTimers());

  it("fires after 500 ms of holding", () => {
    vi.useFakeTimers();
    const fn = vi.fn();
    const lp = new LongPress(fn);
    lp.start(10, 10);
    vi.advanceTimersByTime(LONG_PRESS_MS - 1);
    expect(fn).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(fn).toHaveBeenCalledTimes(1);
    expect(lp.fired).toBe(true);
  });

  it("is cancelled by moving more than 8 px, but not by a jitter", () => {
    vi.useFakeTimers();
    const fn = vi.fn();
    const lp = new LongPress(fn);
    lp.start(10, 10);
    lp.move(14, 12);
    vi.advanceTimersByTime(LONG_PRESS_MS);
    expect(fn).toHaveBeenCalledTimes(1);
    lp.start(10, 10);
    lp.move(30, 10);
    vi.advanceTimersByTime(LONG_PRESS_MS);
    expect(fn).toHaveBeenCalledTimes(1);
  });
});
