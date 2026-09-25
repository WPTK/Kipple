import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  BULK_TTL_MS,
  STACK_MAX,
  STACK_TTL_MS,
  TOAST_MS,
  dismissUndoToast,
  holdUndoToast,
  pushUndo,
  resetUndo,
  undoLast,
  undoStore,
  undoText,
  undoToast,
} from "./undo";

const read = (ids: string[], undo: (ids: string[]) => void | Promise<void> = vi.fn(), extra: Partial<Parameters<typeof pushUndo>[0]> = {}) => pushUndo({ kind: "read", ids, undo, ...extra });
const toast = () => undoStore.get().toast?.text ?? null;

beforeEach(() => {
  vi.useFakeTimers();
  resetUndo();
});
afterEach(() => {
  resetUndo();
  vi.useRealTimers();
});

describe("undo toast", () => {
  it("wording", () => {
    expect(undoText("read", 1, false)).toBe("Marked read");
    expect(undoText("read", 4, false)).toBe("4 articles marked read");
    expect(undoText("unread", 2, false)).toBe("2 articles marked unread");
    expect(undoText("read", 42, true)).toBe("Marked 42 as read");
  });

  it("merges repeated same-kind actions into one toast and undoes the whole batch", async () => {
    const undo = vi.fn();
    read(["1"], undo);
    read(["2"], undo);
    read(["3"], undo);
    expect(toast()).toBe("3 articles marked read");
    await undoToast();
    expect(undo).toHaveBeenCalledTimes(1);
    expect(undo).toHaveBeenCalledWith(["1", "2", "3"]);
    expect(toast()).toBeNull();
    expect(undoStore.get().canUndo).toBe(false);
  });

  it("does not double count the same id", () => {
    read(["1"]);
    read(["1"]);
    expect(toast()).toBe("Marked read");
  });

  it("lasts 15 seconds and each merge restarts the clock", () => {
    read(["1"]);
    vi.advanceTimersByTime(TOAST_MS - 1);
    expect(toast()).not.toBeNull();
    read(["2"]);
    vi.advanceTimersByTime(TOAST_MS - 1);
    expect(toast()).toBe("2 articles marked read");
    vi.advanceTimersByTime(1);
    expect(toast()).toBeNull();
  });

  it("pauses while held (hover or focus) and resumes after", () => {
    read(["1"]);
    holdUndoToast(true);
    vi.advanceTimersByTime(TOAST_MS * 4);
    expect(toast()).not.toBeNull();
    holdUndoToast(false);
    vi.advanceTimersByTime(TOAST_MS);
    expect(toast()).toBeNull();
  });

  it("a different kind replaces the toast; the earlier action stays on the z stack", async () => {
    const a = vi.fn();
    const b = vi.fn();
    read(["1"], a);
    pushUndo({ kind: "unread", ids: ["2"], undo: b });
    expect(toast()).toBe("Marked unread");
    await undoLast();
    expect(b).toHaveBeenCalledWith(["2"]);
    expect(a).not.toHaveBeenCalled();
    await undoLast();
    expect(a).toHaveBeenCalledWith(["1"]);
    expect(await undoLast()).toBe(false);
  });

  it("z still works after the toast has gone, until 60 seconds", async () => {
    const undo = vi.fn();
    read(["1"], undo);
    vi.advanceTimersByTime(TOAST_MS + 1);
    expect(toast()).toBeNull();
    expect(undoStore.get().canUndo).toBe(true);
    vi.advanceTimersByTime(STACK_TTL_MS - TOAST_MS - 2);
    expect(await undoLast()).toBe(true);
    expect(undo).toHaveBeenCalled();
  });

  it("z expires after 60 seconds", async () => {
    read(["1"]);
    vi.advanceTimersByTime(STACK_TTL_MS + 1);
    expect(await undoLast()).toBe(false);
  });

  it("keeps at most 20 groups", async () => {
    const undo = vi.fn();
    for (let i = 0; i < 30; i++) pushUndo({ kind: i % 2 ? "unread" : "read", ids: [String(i)], undo });
    let n = 0;
    while (await undoLast()) n++;
    expect(n).toBe(STACK_MAX);
  });

  it("bulk batches never merge and stay undoable for 2 minutes", async () => {
    const undo = vi.fn();
    read(["1"]);
    read(["2", "3", "4"], undo, { bulk: true });
    expect(toast()).toBe("Marked 3 as read");
    read(["5"]);
    expect(toast()).toBe("Marked read"); // a fresh single, not merged into the bulk
    vi.advanceTimersByTime(STACK_TTL_MS + 5_000);
    await undoLast(); // the single "5" is past 60 s? it is pruned; the bulk is still inside 2 minutes
    expect(undo).toHaveBeenCalledWith(["2", "3", "4"]);
  });

  it("bulk expires at 2 minutes", async () => {
    read(["1", "2"], vi.fn(), { bulk: true });
    vi.advanceTimersByTime(BULK_TTL_MS + 1);
    expect(await undoLast()).toBe(false);
  });

  it("runs the UI restore hooks (a hidden row comes back) before the API undo", async () => {
    const order: string[] = [];
    read(["1"], () => void order.push("api"), { restore: () => order.push("restore-1") });
    read(["2"], () => void order.push("api"), { restore: () => order.push("restore-2") });
    await undoToast();
    expect(order).toEqual(["restore-1", "restore-2", "api"]);
  });

  it("dismiss removes the toast but keeps the stack", async () => {
    read(["1"]);
    dismissUndoToast();
    expect(toast()).toBeNull();
    expect(undoStore.get().canUndo).toBe(true);
  });
});
