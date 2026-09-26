import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement, createRef } from "react";
import { ListenBar } from "@/screens/ListenBar";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { Narrator, chunksOf, narratorStore, splitText } from "./speech";

class FakeUtterance {
  onend: (() => void) | null = null;
  onerror: ((e: { error: string }) => void) | null = null;
  rate = 1;
  voice: unknown = null;
  lang = "";
  constructor(public text: string) {}
}

function fakeSynth() {
  const spoken: FakeUtterance[] = [];
  const synth = {
    speaking: false,
    speak: vi.fn((u: FakeUtterance) => {
      spoken.push(u);
      synth.speaking = true;
    }),
    cancel: vi.fn(() => {
      synth.speaking = false;
    }),
    pause: vi.fn(),
    resume: vi.fn(),
    getVoices: () => [] as unknown[],
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  };
  return { synth, spoken };
}

let env: ReturnType<typeof fakeSynth>;
beforeEach(() => {
  env = fakeSynth();
  vi.stubGlobal("speechSynthesis", env.synth);
  vi.stubGlobal("SpeechSynthesisUtterance", FakeUtterance);
  Object.defineProperty(window, "speechSynthesis", { configurable: true, value: env.synth });
  prefsStore.set({ ...DEFAULT_PREFS, listen: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  Reflect.deleteProperty(window, "speechSynthesis");
  narratorStore.set({ status: "idle", index: 0, total: 0 });
});

describe("splitText", () => {
  it("keeps short paragraphs whole and splits long ones at sentences", () => {
    expect(splitText("Short one.")).toEqual(["Short one."]);
    const long = Array.from({ length: 12 }, (_, i) => `This is sentence number ${i} of a long paragraph.`).join(" ");
    const parts = splitText(long, 100);
    expect(parts.length).toBeGreaterThan(3);
    expect(parts.every((p) => p.length <= 100)).toBe(true);
    expect(parts.join(" ")).toBe(long);
  });
  it("breaks an unpunctuated run at spaces", () => {
    const parts = splitText("word ".repeat(100), 60);
    expect(parts.every((p) => p.length <= 60)).toBe(true);
  });
});

describe("chunksOf", () => {
  it("takes readable blocks in order and skips code and wrappers", () => {
    const root = document.createElement("div");
    root.innerHTML = "<h2>Title</h2><p>One.</p><blockquote><p>Quoted.</p></blockquote><pre>code()</pre><ul><li>Item</li></ul>";
    expect(chunksOf(root).map((c) => c.text)).toEqual(["Title", "One.", "Quoted.", "Item"]);
  });
});

describe("Narrator", () => {
  it("speaks chunk by chunk, highlights the current paragraph, and stops cleanly", () => {
    const root = document.createElement("div");
    root.innerHTML = "<p>First.</p><p>Second.</p>";
    document.body.appendChild(root);
    const n = new Narrator();
    n.setOptions({ rate: 1.2, voiceURI: "" });
    n.start(chunksOf(root));
    expect(env.spoken[0]?.text).toBe("First.");
    expect(env.spoken[0]?.rate).toBe(1.2);
    expect(root.children[0]?.classList.contains("kp-speaking")).toBe(true);
    env.spoken[0]?.onend?.();
    expect(env.spoken[1]?.text).toBe("Second.");
    expect(root.children[1]?.classList.contains("kp-speaking")).toBe(true);
    expect(root.children[0]?.classList.contains("kp-speaking")).toBe(false);
    expect(narratorStore.get()).toMatchObject({ status: "playing", index: 1, total: 2 });
    n.pause();
    expect(narratorStore.get().status).toBe("paused");
    n.resume();
    expect(narratorStore.get().status).toBe("playing");
    env.spoken[1]?.onend?.();
    expect(narratorStore.get().status).toBe("idle");
    expect(root.querySelector(".kp-speaking")).toBeNull();
  });

  it("skips backward and forward", () => {
    const root = document.createElement("div");
    root.innerHTML = "<p>A.</p><p>B.</p><p>C.</p>";
    const n = new Narrator();
    n.start(chunksOf(root));
    n.skip(1);
    n.skip(1);
    expect(env.spoken.at(-1)?.text).toBe("C.");
    n.skip(1); // past the end: ignored
    n.skip(-1);
    expect(env.spoken.at(-1)?.text).toBe("B.");
    n.stop();
  });
});

describe("ListenBar", () => {
  it("shows Listen, then play, pause and stop controls, and the iOS caveat", async () => {
    const ref = createRef<HTMLDivElement>();
    const host = document.createElement("div");
    host.innerHTML = "<p>Hello there.</p>";
    ref.current = host;
    render(createElement(ListenBar, { bodyRef: ref, articleId: "1" }));
    const user = userEvent.setup();
    expect(screen.getByText(/On iPhone, reading stops/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Listen" }));
    await user.click(await screen.findByRole("button", { name: "Pause" }));
    expect(await screen.findByRole("button", { name: "Resume" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Stop" }));
    expect(screen.getByRole("button", { name: "Listen" })).toBeInTheDocument();
  });

  it("turning the setting off mid-playback stops the speech", async () => {
    const ref = createRef<HTMLDivElement>();
    const host = document.createElement("div");
    host.innerHTML = "<p>Hello there.</p>";
    ref.current = host;
    render(createElement(ListenBar, { bodyRef: ref, articleId: "1" }));
    await userEvent.setup().click(screen.getByRole("button", { name: "Listen" }));
    expect(narratorStore.get().status).toBe("playing");
    env.synth.cancel.mockClear();
    act(() => prefsStore.set({ ...prefsStore.get(), listen: false }));
    expect(env.synth.cancel).toHaveBeenCalled();
    expect(narratorStore.get().status).toBe("idle");
  });

  it("renders nothing when speech is unsupported or the setting is off", () => {
    const ref = createRef<HTMLDivElement>();
    prefsStore.set({ ...DEFAULT_PREFS, listen: false });
    const { container, unmount } = render(createElement(ListenBar, { bodyRef: ref, articleId: "1" }));
    expect(container).toBeEmptyDOMElement();
    unmount();
    prefsStore.set({ ...DEFAULT_PREFS, listen: true });
    Reflect.deleteProperty(window, "speechSynthesis");
    vi.stubGlobal("SpeechSynthesisUtterance", undefined);
    const r2 = render(createElement(ListenBar, { bodyRef: ref, articleId: "1" }));
    expect(r2.container).toBeEmptyDOMElement();
  });
});
