import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  BULK_TTL_MS,
  STACK_MAX,
  STACK_TTL_MS,
  NOTHING_TO_UNDO,
  TOAST_MS,
  UNDO_WAIT_MS,
  beginUndo,
  dismissUndoToast,
  holdUndoToast,
  pushUndo,
  resetUndo,
  undoLast,
  undoStore,
  undoText,
  undoToast,
} from "./undo";
import * as toasts from "@/shell/toasts";

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

describe("undo failures", () => {
  it("a rejected or false undo keeps the group and does not say Undone", async () => {
    const undo = vi.fn().mockResolvedValueOnce(false).mockRejectedValueOnce(new Error("net")).mockResolvedValue(undefined);
    read(["1"], undo);
    await undoToast();
    expect(undoStore.get().canUndo).toBe(true);
    expect(toast()).toBe("Marked read");
    await undoLast();
    expect(undoStore.get().canUndo).toBe(true);
    await undoLast();
    expect(undoStore.get().canUndo).toBe(false);
    expect(undo).toHaveBeenCalledTimes(3);
  });
});

describe("actions still in flight", () => {
  it("a same-kind action joining the toast's group while in flight is waited for and undone with it", async () => {
    const undo = vi.fn();
    read(["1"], undo);
    const t = beginUndo("read");
    expect(toast()).toBe("Marked read"); // the settled part is still offered
    const z = undoLast();
    expect(toast()).toBe("Undoing…");
    t.settle({ ids: ["2"], undo: vi.fn() });
    await z;
    expect(undo).toHaveBeenCalledWith(["1", "2"]);
    expect(undoStore.get().canUndo).toBe(false);
  });

  it("z with nothing to undo says so where it can be seen", async () => {
    const shown = vi.spyOn(toasts, "toast");
    expect(await undoLast()).toBe(false);
    expect(shown).toHaveBeenCalledWith(NOTHING_TO_UNDO);
  });

  it("a group pushed off the stack while in flight does not land on top when it settles", async () => {
    const bulkUndo = vi.fn();
    const t = beginUndo("read", { bulk: true });
    const last = vi.fn();
    for (let i = 0; i < STACK_MAX; i++) pushUndo({ kind: i % 2 ? "unread" : "read", ids: [String(i)], undo: i === STACK_MAX - 1 ? last : vi.fn() });
    t.settle({ ids: ["b"], undo: bulkUndo });
    await undoLast();
    expect(last).toHaveBeenCalled();
    expect(bulkUndo).not.toHaveBeenCalled();
  });

  it("a waiting z still undoes its group when it lands, even if the group was pushed off meanwhile", async () => {
    const shown = vi.spyOn(toasts, "toast");
    const bulkUndo = vi.fn();
    const t = beginUndo("read", { bulk: true });
    const z = undoLast();
    for (let i = 0; i < STACK_MAX; i++) pushUndo({ kind: i % 2 ? "unread" : "read", ids: [String(i)], undo: vi.fn() });
    t.settle({ ids: ["b"], undo: bulkUndo });
    await z;
    expect(bulkUndo).toHaveBeenCalledWith(["b"]);
    expect(shown).not.toHaveBeenCalledWith(NOTHING_TO_UNDO);
  });

  it("the toast's clock pauses while Undoing… shows and gets a full window when the wait gives up", async () => {
    read(["1"]);
    await vi.advanceTimersByTimeAsync(10_000); // 5 s left on the toast
    const t = beginUndo("read"); // joins the toast's group, still in flight
    const z = undoLast();
    await vi.advanceTimersByTimeAsync(UNDO_WAIT_MS);
    await z;
    expect(toast()).toBe("Marked read");
    await vi.advanceTimersByTimeAsync(TOAST_MS - 1_000);
    expect(toast()).toBe("Marked read");
    await vi.advanceTimersByTimeAsync(1_000);
    expect(toast()).toBeNull();
    t.settle(null);
  });

  it("a join does not restart the toast's clock, and the joined action shows the toast again when it lands", async () => {
    read(["1"]);
    await vi.advanceTimersByTimeAsync(14_800);
    const t = beginUndo("read"); // joins the toast's group
    await vi.advanceTimersByTimeAsync(400);
    expect(toast()).toBeNull(); // the clock ran out between join and landing (a restart on join would still show it)
    t.settle({ ids: ["2"], undo: vi.fn() });
    expect(toast()).toBe("2 articles marked read");
    await vi.advanceTimersByTimeAsync(TOAST_MS - 100);
    expect(toast()).toBe("2 articles marked read");
    await vi.advanceTimersByTimeAsync(100);
    expect(toast()).toBeNull();
  });

  it("a joined action that lands after a newer toast does not take the toast back", async () => {
    read(["1"]);
    const t = beginUndo("read");
    pushUndo({ kind: "star", ids: ["9"], undo: vi.fn() });
    t.settle({ ids: ["2"], undo: vi.fn() });
    expect(toast()).toBe("Starred");
  });

  it("a failed undo goes back in the order the user acted, below a newer group, which keeps its toast", async () => {
    let fail!: (ok: boolean) => void;
    const starUndo = vi.fn().mockImplementationOnce(() => new Promise<boolean>((r) => (fail = r))).mockResolvedValue(undefined);
    const newUndo = vi.fn();
    const a = beginUndo("read"); // in flight
    pushUndo({ kind: "star", ids: ["9"], undo: starUndo });
    const z = undoToast(); // the star's undo is out
    pushUndo({ kind: "read", ids: ["5"], undo: newUndo }); // N
    a.settle(null); // A leaves the stack
    fail(false);
    await z;
    expect(toast()).toBe("Marked read"); // N keeps its toast
    await undoLast();
    expect(newUndo).toHaveBeenCalledWith(["5"]); // stack is [S, N]: N first
    expect(starUndo).toHaveBeenCalledTimes(1);
    await undoLast();
    expect(starUndo).toHaveBeenCalledTimes(2);
  });

  it("a newer group whose undo fails gets its toast back from an older one that landed meanwhile", async () => {
    const readUndo = vi.fn();
    let fail!: (ok: boolean) => void;
    const starUndo = vi.fn().mockImplementationOnce(() => new Promise<boolean>((r) => (fail = r))).mockResolvedValue(undefined);
    const a = beginUndo("read"); // slow
    pushUndo({ kind: "star", ids: ["9"], undo: starUndo });
    const z = undoToast(); // the star's toast
    a.settle({ ids: ["1"], undo: readUndo });
    expect(toast()).toBe("Marked read"); // the star is out being undone
    fail(false);
    await z;
    expect(toast()).toBe("Starred");
    await undoToast();
    expect(starUndo).toHaveBeenCalledTimes(2);
    expect(readUndo).not.toHaveBeenCalled();
  });

  it("a pushed-off group whose undo fails does not come back on top of the stack", async () => {
    const bulkUndo = vi.fn().mockResolvedValue(false);
    const t = beginUndo("read", { bulk: true });
    const z = undoLast();
    const last = vi.fn();
    for (let i = 0; i < STACK_MAX; i++) pushUndo({ kind: i % 2 ? "unread" : "read", ids: [String(i)], undo: i === STACK_MAX - 1 ? last : vi.fn() });
    t.settle({ ids: ["b"], undo: bulkUndo });
    await z;
    expect(bulkUndo).toHaveBeenCalledTimes(1);
    expect(toast()).not.toBe("Marked 1 as read");
    await undoLast();
    expect(last).toHaveBeenCalled();
    expect(bulkUndo).toHaveBeenCalledTimes(1);
  });

  it("a group whose window ended while in flight shows no toast when it lands", async () => {
    const undo = vi.fn();
    const t = beginUndo("read");
    await vi.advanceTimersByTimeAsync(STACK_TTL_MS + 1_000);
    t.settle({ ids: ["1"], undo });
    expect(toast()).toBeNull();
    expect(undoStore.get().canUndo).toBe(false);
    await undoToast();
    expect(await undoLast()).toBe(false);
    expect(undo).not.toHaveBeenCalled();
  });

  it("a failing quick action does not lose the toast of a slower one still in flight", () => {
    const m = beginUndo("read");
    const s = beginUndo("star");
    s.settle(null);
    m.settle({ ids: ["1"], undo: vi.fn() });
    expect(toast()).toBe("Marked read");
  });

  it("an in-flight action that settles with nothing leaves no group behind", () => {
    beginUndo("read", { bulk: true }).settle(null);
    expect(undoStore.get().canUndo).toBe(false);
    expect(toast()).toBeNull();
  });
});
