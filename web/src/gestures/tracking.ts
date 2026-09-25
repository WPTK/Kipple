// Pure gesture math and state machines, no DOM. The components feed them
// pointer or touch samples, and the tests feed them synthetic ones.
// Numbers come from docs/research/ui-layouts-keymap-density-round2.md section 3.

export const EDGE_DEAD_LEFT_PX = 24; // iOS owns the left edge (back gesture)
export const EDGE_DEAD_RIGHT_PX = 8;

export const ROW = {
  lockPx: 8,
  lockRatio: 1.5,
  iconFadePx: 24,
  armMin: 96,
  armFrac: 0.4,
  armMax: 176,
  flickVelocity: 0.6, // px/ms
  flickMinTravel: 64,
  reverseVelocity: 0.4, // px/ms back toward the origin cancels even when armed
  rubberFrac: 0.6,
  rubber: 0.5,
  revealWidth: 144, // trailing panel: Star + More, 72 px each
  revealSnap: 60, // release past this and the trailing panel stays open
} as const;

export const BACK = {
  startMin: EDGE_DEAD_LEFT_PX,
  lockPx: 10,
  lockRatio: 2,
  commitFrac: 0.35,
  flickVelocity: 0.5,
  flickMinTravel: 64,
} as const;

export const PULL = {
  showAt: 16,
  armAt: 72,
  max: 120,
  resistance: 0.5,
  lockPx: 6,
  preventDefaultAfter: 12,
} as const;

export const LONG_PRESS_MS = 500;
export const LONG_PRESS_SLOP_PX = 8;

export const clamp = (v: number, lo: number, hi: number): number => Math.min(hi, Math.max(lo, v));

/** Distance at which a row swipe is armed: 40% of the row, between 96 and 176 px. */
export const armDistance = (width: number): number => clamp(width * ROW.armFrac, ROW.armMin, ROW.armMax);

/** Rubber band: past 60% of the row the finger has half the effect. */
export function rubberBand(offset: number, width: number): number {
  const a = Math.abs(offset);
  const knee = width * ROW.rubberFrac;
  if (a <= knee) return offset;
  return Math.sign(offset) * (knee + (a - knee) * ROW.rubber);
}

/** Velocity in px/ms over the last ~100 ms of samples (signed, +x is rightward). */
export class VelocityTracker {
  private s: { x: number; t: number }[] = [];
  reset(): void {
    this.s = [];
  }
  push(x: number, t: number): void {
    this.s.push({ x, t });
    while (this.s.length > 2 && t - (this.s[0] as { t: number }).t > 100) this.s.shift();
  }
  velocity(): number {
    if (this.s.length < 2) return 0;
    const a = this.s[0] as { x: number; t: number };
    const b = this.s[this.s.length - 1] as { x: number; t: number };
    return (b.x - a.x) / Math.max(1, b.t - a.t);
  }
}

// ---------------------------------------------------------------- row swipe

export type RowPhase = "idle" | "pending" | "horizontal" | "vertical" | "ignored";
export type RowEnd =
  | { action: "commit"; side: "leading" | "trailing" }
  | { action: "open" }
  | { action: "close" };

export interface RowStartOpts {
  /** Width of the row, px. */
  width: number;
  /** Viewport width, for the right-edge dead zone. */
  viewportWidth: number;
  /** Where the row content rests before this touch: 0 closed, -revealWidth when the trailing panel is open. */
  base?: number;
  /** More than one finger down. */
  multiTouch?: boolean;
}

export class RowSwipe {
  phase: RowPhase = "idle";
  private x0 = 0;
  private y0 = 0;
  private width = 0;
  private base = 0;
  private vt = new VelocityTracker();
  /** Content offset (px, signed, before rubber banding). */
  offset = 0;

  start(x: number, y: number, t: number, o: RowStartOpts): void {
    this.x0 = x;
    this.y0 = y;
    this.width = o.width;
    this.base = o.base ?? 0;
    this.offset = this.base;
    this.vt.reset();
    this.vt.push(x, t);
    const dead = x < EDGE_DEAD_LEFT_PX || x > o.viewportWidth - EDGE_DEAD_RIGHT_PX;
    this.phase = o.multiTouch || dead ? "ignored" : "pending";
  }

  /** Returns the phase after this sample. Once horizontal, `offset` follows the finger. */
  move(x: number, y: number, t: number): RowPhase {
    if (this.phase === "idle" || this.phase === "ignored" || this.phase === "vertical") return this.phase;
    const dx = x - this.x0;
    const dy = y - this.y0;
    if (this.phase === "pending") {
      if (Math.abs(dx) < ROW.lockPx && Math.abs(dy) < ROW.lockPx) return "pending";
      // Direction lock: horizontal only when clearly sideways, else release to vertical scroll.
      this.phase = Math.abs(dx) > ROW.lockRatio * Math.abs(dy) ? "horizontal" : "vertical";
      if (this.phase === "vertical") return this.phase;
    }
    this.vt.push(x, t);
    this.offset = this.base + dx;
    return "horizontal";
  }

  /** Cancel (second finger, pointercancel). */
  cancel(): void {
    this.phase = "idle";
  }

  armed(): boolean {
    return Math.abs(this.offset) >= armDistance(this.width);
  }

  end(): RowEnd {
    const wasHorizontal = this.phase === "horizontal";
    this.phase = "idle";
    if (!wasHorizontal) return { action: "close" };
    const off = this.offset;
    const side = off > 0 ? "leading" : "trailing";
    const dir = Math.sign(off);
    if (dir === 0) return { action: "close" };
    const travel = Math.abs(off);
    const outward = this.vt.velocity() * dir; // >0 = still moving away from the origin
    if (outward <= -ROW.reverseVelocity) return { action: "close" };
    if (travel >= armDistance(this.width)) return { action: "commit", side };
    if (outward >= ROW.flickVelocity && travel >= ROW.flickMinTravel) return { action: "commit", side };
    if (side === "trailing" && travel >= ROW.revealSnap) return { action: "open" };
    return { action: "close" };
  }
}

// ----------------------------------------------------------------- back swipe

export type BackPhase = "idle" | "pending" | "horizontal" | "ignored";

export class BackSwipe {
  phase: BackPhase = "idle";
  private x0 = 0;
  private y0 = 0;
  private width = 0;
  private vt = new VelocityTracker();
  dx = 0;

  /** `blocked` is the DOM's verdict: scrollable content, selection, controls, pinch. */
  start(x: number, y: number, t: number, width: number, blocked: boolean): void {
    this.x0 = x;
    this.y0 = y;
    this.width = width;
    this.dx = 0;
    this.vt.reset();
    this.vt.push(x, t);
    this.phase = blocked || x < BACK.startMin ? "ignored" : "pending";
  }

  move(x: number, y: number, t: number): BackPhase {
    if (this.phase === "idle" || this.phase === "ignored") return this.phase;
    const dx = x - this.x0;
    const dy = y - this.y0;
    if (this.phase === "pending") {
      if (Math.hypot(dx, dy) < BACK.lockPx) return "pending";
      // Rightward only, and within about 27 degrees of horizontal.
      if (dx <= 0 || dx <= BACK.lockRatio * Math.abs(dy)) {
        this.phase = "ignored";
        return this.phase;
      }
      this.phase = "horizontal";
    }
    this.vt.push(x, t);
    this.dx = Math.max(0, dx);
    return "horizontal";
  }

  cancel(): void {
    this.phase = "idle";
  }

  /** True when the release should pop to the previous screen. */
  end(): boolean {
    const was = this.phase === "horizontal";
    this.phase = "idle";
    if (!was) return false;
    const v = this.vt.velocity();
    if (v < 0) return false;
    return this.dx >= this.width * BACK.commitFrac || (v >= BACK.flickVelocity && this.dx >= BACK.flickMinTravel);
  }
}

// -------------------------------------------------------------- pull to refresh

export type PullPhase = "idle" | "pending" | "pulling" | "dead";

export class PullTracker {
  phase: PullPhase = "idle";
  private x0 = 0;
  private y0 = 0;
  /** Displayed pull distance after resistance, 0 to PULL.max. */
  distance = 0;

  /** Only a touch that starts with the list scrolled to the very top can pull. */
  start(x: number, y: number, scrollTop: number): void {
    this.x0 = x;
    this.y0 = y;
    this.distance = 0;
    this.phase = scrollTop <= 0 ? "pending" : "dead";
  }

  /** `rowSwipeLocked`: a row swipe took the touch first, so pulling is dead for it. */
  move(x: number, y: number, scrollTop: number, rowSwipeLocked = false): PullPhase {
    if (this.phase === "idle" || this.phase === "dead") return this.phase;
    if (scrollTop > 0 || rowSwipeLocked) {
      this.phase = "dead";
      this.distance = 0;
      return this.phase;
    }
    const dx = x - this.x0;
    const dy = y - this.y0;
    if (this.phase === "pending") {
      if (Math.abs(dy) < PULL.lockPx && Math.abs(dx) < PULL.lockPx) return "pending";
      if (dy <= 0 || Math.abs(dy) <= Math.abs(dx)) {
        this.phase = "dead";
        return this.phase;
      }
      this.phase = "pulling";
    }
    if (dy <= 0) {
      this.phase = "dead";
      this.distance = 0;
      return this.phase;
    }
    this.distance = Math.min(PULL.max, dy * PULL.resistance);
    return "pulling";
  }

  visible(): boolean {
    return this.phase === "pulling" && this.distance >= PULL.showAt;
  }
  armed(): boolean {
    return this.phase === "pulling" && this.distance >= PULL.armAt;
  }
  /** Whether the native scroll should be blocked (Safari tab rubber band). */
  shouldPreventDefault(): boolean {
    return this.phase === "pulling" && this.distance / PULL.resistance >= PULL.preventDefaultAfter;
  }

  end(): boolean {
    const go = this.armed();
    this.phase = "idle";
    this.distance = 0;
    return go;
  }
}

// ----------------------------------------------------------------- long press

/** Fires `onFire` after 500 ms of holding without moving more than 8 px. */
export class LongPress {
  private timer: ReturnType<typeof setTimeout> | undefined;
  private x = 0;
  private y = 0;
  fired = false;
  constructor(public onFire: () => void = () => undefined) {}
  start(x: number, y: number): void {
    this.cancel();
    this.fired = false;
    this.x = x;
    this.y = y;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      this.fired = true;
      this.onFire();
    }, LONG_PRESS_MS);
  }
  move(x: number, y: number): void {
    if (this.timer && Math.hypot(x - this.x, y - this.y) > LONG_PRESS_SLOP_PX) this.cancel();
  }
  cancel(): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = undefined;
  }
}

export function prefersReducedMotion(): boolean {
  try {
    return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  } catch {
    return false;
  }
}
