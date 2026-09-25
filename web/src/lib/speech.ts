import { createStore } from "./store";

// Read aloud through the Web Speech API. Client only, no server cost. The article is split into chunks
// (a paragraph, or a sentence run for very long paragraphs) that are queued one at a time, because long
// utterances get cut off on older iOS. The chunk being spoken is highlighted (a paragraph, not a word:
// word boundary events are unreliable on iOS).

export const MAX_CHUNK = 240;

export function speechSupported(): boolean {
  return typeof window !== "undefined" && "speechSynthesis" in window && typeof SpeechSynthesisUtterance !== "undefined";
}

/** Split text into pieces of at most `max` characters, breaking at sentence ends, then at spaces. */
export function splitText(text: string, max = MAX_CHUNK): string[] {
  const clean = text.replace(/\s+/g, " ").trim();
  if (!clean) return [];
  if (clean.length <= max) return [clean];
  const sentences = clean.match(/[^.!?]+[.!?]+["')\]]*\s*|[^.!?]+$/g) ?? [clean];
  const out: string[] = [];
  let cur = "";
  const flush = () => {
    if (cur.trim()) out.push(cur.trim());
    cur = "";
  };
  for (const s of sentences) {
    if (s.length > max) {
      flush();
      let rest = s;
      while (rest.length > max) {
        let cut = rest.lastIndexOf(" ", max);
        if (cut < max / 2) cut = max;
        out.push(rest.slice(0, cut).trim());
        rest = rest.slice(cut);
      }
      cur = rest;
      continue;
    }
    if ((cur + s).length > max) flush();
    cur += s;
  }
  flush();
  return out;
}

export interface Chunk {
  el: Element;
  text: string;
}

/** The readable blocks of an article body, in order, each split to a speakable size. */
export function chunksOf(root: Element): Chunk[] {
  const out: Chunk[] = [];
  root.querySelectorAll("h1,h2,h3,h4,h5,h6,p,li,blockquote,figcaption").forEach((el) => {
    // A block that contains other blocks is spoken through its children.
    if (el.querySelector("p,li,blockquote,h1,h2,h3,h4,h5,h6")) return;
    if (el.closest("pre")) return;
    for (const text of splitText(el.textContent ?? "")) out.push({ el, text });
  });
  return out;
}

export interface NarratorState {
  status: "idle" | "playing" | "paused";
  index: number;
  total: number;
}

export const narratorStore = createStore<NarratorState>({ status: "idle", index: 0, total: 0 });

const CLASS = "kp-speaking";

export interface SpeakOptions {
  rate: number;
  voiceURI: string;
}

/** Queue-and-highlight controller around speechSynthesis. One instance per article view. */
export class Narrator {
  private chunks: Chunk[] = [];
  private i = 0;
  private gen = 0;
  private opts: SpeakOptions = { rate: 1, voiceURI: "" };

  setOptions(o: SpeakOptions): void {
    this.opts = o;
  }

  start(chunks: Chunk[], from = 0): void {
    this.stop();
    if (!speechSupported() || chunks.length === 0) return;
    this.chunks = chunks;
    this.i = Math.min(Math.max(0, from), chunks.length - 1);
    narratorStore.set({ status: "playing", index: this.i, total: chunks.length });
    this.speak();
  }

  private speak(): void {
    const gen = ++this.gen;
    const c = this.chunks[this.i];
    if (!c) return this.stop();
    this.mark(c.el);
    const u = new SpeechSynthesisUtterance(c.text);
    const { rate, voiceURI } = this.opts;
    u.rate = rate;
    const v = voiceURI ? speechSynthesis.getVoices().find((x) => x.voiceURI === voiceURI) : undefined;
    if (v) {
      u.voice = v;
      u.lang = v.lang;
    }
    u.onend = () => {
      if (gen !== this.gen) return;
      if (this.i + 1 >= this.chunks.length) return this.stop();
      this.i += 1;
      narratorStore.set((s) => ({ ...s, index: this.i }));
      this.speak();
    };
    u.onerror = (e) => {
      if (gen !== this.gen) return;
      // "canceled" and "interrupted" come from our own cancel(); anything else ends the session.
      if (e.error !== "canceled" && e.error !== "interrupted") this.stop();
    };
    speechSynthesis.cancel();
    speechSynthesis.speak(u);
  }

  private mark(el: Element | null): void {
    el?.ownerDocument.querySelectorAll(`.${CLASS}`).forEach((n) => n.classList.remove(CLASS));
    el?.classList.add(CLASS);
    // Keep the spoken paragraph in view without stealing focus.
    el?.scrollIntoView?.({ block: "center", behavior: "auto" });
  }

  pause(): void {
    if (narratorStore.get().status !== "playing") return;
    speechSynthesis.pause();
    narratorStore.set((s) => ({ ...s, status: "paused" }));
  }

  resume(): void {
    if (narratorStore.get().status !== "paused") return;
    speechSynthesis.resume();
    // iOS sometimes ignores resume(): if nothing is speaking, restart the current chunk.
    if (!speechSynthesis.speaking) this.speak();
    narratorStore.set((s) => ({ ...s, status: "playing" }));
  }

  skip(delta: 1 | -1): void {
    if (narratorStore.get().status === "idle") return;
    const next = this.i + delta;
    if (next < 0 || next >= this.chunks.length) return;
    this.i = next;
    narratorStore.set((s) => ({ ...s, index: next, status: "playing" }));
    this.speak();
  }

  /** Restart the current chunk with new options (a rate or voice change while playing). */
  refresh(): void {
    if (narratorStore.get().status === "playing") this.speak();
  }

  stop(): void {
    this.gen += 1;
    if (speechSupported()) speechSynthesis.cancel();
    this.mark(null);
    document.querySelectorAll(`.${CLASS}`).forEach((n) => n.classList.remove(CLASS));
    this.chunks = [];
    this.i = 0;
    narratorStore.set({ status: "idle", index: 0, total: 0 });
  }
}

/** English voices first (Kipple's readers are English), then the rest. */
export function listVoices(): SpeechSynthesisVoice[] {
  if (!speechSupported()) return [];
  const vs = [...speechSynthesis.getVoices()];
  const en = (v: SpeechSynthesisVoice) => (v.lang.toLowerCase().startsWith("en") ? 0 : 1);
  return vs.sort((a, b) => en(a) - en(b) || a.name.localeCompare(b.name));
}
