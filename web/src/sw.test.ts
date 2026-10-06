import { describe, expect, it, vi } from "vitest";
import swSource from "../sw/sw.js?raw";
import { SIGN_IN_RELOAD } from "@/lib/reload";

// Runs web/sw/sw.js against a fake worker scope: an in-memory CacheStorage and a scripted network.

const ORIGIN = "https://kipple.test";
type Handler = (e: unknown) => void;

function abs(r: string | { url: string }): string {
  return new URL(typeof r === "string" ? r : r.url, ORIGIN).href;
}

interface Stored {
  status: number;
  statusText: string;
  headers: [string, string][];
  body: string;
}

function fakeCache() {
  const m = new Map<string, Stored>();
  return {
    async put(req: string | { url: string }, res: Response) {
      m.set(abs(req), { status: res.status, statusText: res.statusText, headers: [...res.headers.entries()], body: await res.text() });
    },
    async match(req: string | { url: string }) {
      const s = m.get(abs(req));
      return s ? new Response(s.body, { status: s.status, statusText: s.statusText, headers: s.headers }) : undefined;
    },
    async keys() {
      return [...m.keys()].map((url) => ({ url }));
    },
    async delete(req: string | { url: string }) {
      return m.delete(abs(req));
    },
  };
}

/**
 * `attached`: a deleted cache's open handles still write under its name (as if the worker opened it again by name
 * after the delete). The spec orphans such handles, but a request that opens the cache after a clear, or an engine
 * that does not orphan, lands in the same place, so the worker must not rely on orphaning.
 */
function setup(precache: string[] = [], build = "0000000000001-aaaa", opts: { attached?: boolean } = {}) {
  const stores = new Map<string, ReturnType<typeof fakeCache>>();
  const handles = new Map<string, ReturnType<typeof fakeCache>>();
  const open = (name: string) => {
    if (!stores.has(name)) stores.set(name, (opts.attached && handles.get(name)) || fakeCache());
    handles.set(name, stores.get(name)!);
    return stores.get(name)!;
  };
  const caches = {
    open: async (n: string) => open(n),
    keys: async () => [...stores.keys()],
    delete: async (n: string) => {
      const c = stores.get(n);
      if (opts.attached && c) {
        for (const k of await c.keys()) await c.delete(k.url);
        const put = c.put.bind(c);
        // A put through the old handle files the entry under the name again.
        c.put = async (req, res) => {
          stores.set(n, c);
          await put(req, res);
        };
      }
      return stores.delete(n);
    },
    match: async (req: { url: string }) => {
      for (const c of stores.values()) {
        const hit = await c.match(req);
        if (hit) return hit;
      }
      return undefined;
    },
  };
  const handlers = new Map<string, Handler>();
  let skipped = 0;
  const self = {
    location: { origin: ORIGIN },
    addEventListener: (t: string, f: Handler) => handlers.set(t, f),
    skipWaiting: async () => {
      skipped++;
    },
    clients: { claim: async () => {} },
  };
  let network: (url: string) => Promise<Response> = async () => {
    throw new TypeError("offline");
  };
  const fetchFn = (input: string | { url: string }) => network(abs(input));
  const src = swSource.replace('/*BUILD*/ "dev"', JSON.stringify(build)).replace("/*PRECACHE*/ []", JSON.stringify(precache));
  new Function("self", "caches", "fetch", src)(self, caches, fetchFn);

  const waits: Promise<unknown>[] = [];
  const settle = async () => {
    while (waits.length) await waits.shift();
  };
  return {
    stores,
    skipped: () => skipped,
    setNetwork: (f: (url: string) => Promise<Response> | Response) => (network = async (u) => f(u)),
    lifecycle: async (type: "install" | "activate") => {
      handlers.get(type)!({ waitUntil: (p: Promise<unknown>) => waits.push(p) });
      await settle();
    },
    message: async (data: unknown) => {
      handlers.get("message")!({ data, waitUntil: (p: Promise<unknown>) => waits.push(p) });
      await settle();
    },
    /** Fire a fetch event; undefined when the worker left the request to the browser. */
    fetch: async (url: string, init: { method?: string; mode?: string } = {}): Promise<Response | undefined> => {
      let answer: Promise<Response> | undefined;
      handlers.get("fetch")!({
        request: { method: init.method ?? "GET", url: abs(url), mode: init.mode ?? "cors" },
        respondWith: (p: Promise<Response>) => (answer = p),
        waitUntil: (p: Promise<unknown>) => waits.push(p),
      });
      const res = answer ? await answer : undefined;
      await settle();
      return res;
    },
  };
}

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

describe("install and activate", () => {
  it("precaches the shell, all or nothing", async () => {
    const w = setup(["/", "/assets/a.js"]);
    w.setNetwork((u) => new Response("body of " + u));
    await w.lifecycle("install");
    expect(await (await w.stores.get("kipple-shell-0000000000001-aaaa")!.match("/assets/a.js"))!.text()).toBe(`body of ${ORIGIN}/assets/a.js`);

    const bad = setup(["/", "/assets/gone.js"]);
    bad.setNetwork((u) => (u.endsWith("gone.js") ? new Response("no", { status: 404 }) : new Response("ok")));
    await expect(bad.lifecycle("install")).rejects.toThrow("404");
    expect([...bad.stores.keys()]).toEqual([]); // no half-filled shell left for an offline launch to prefer
  });

  it("keeps this build's shell and the one before, drops older ones", async () => {
    const w = setup([], "0000000000004-dddd");
    for (const b of ["1", "2", "3", "4"]) w.stores.set(`kipple-shell-000000000000${b}-x`, fakeCache());
    w.stores.set("kipple-shell-0000000000004-dddd", fakeCache());
    w.stores.set("kipple-data", fakeCache());
    await w.lifecycle("activate");
    expect([...w.stores.keys()].sort()).toEqual(["kipple-data", "kipple-shell-0000000000004-dddd", "kipple-shell-0000000000004-x"]);
  });
});

describe("what it leaves to the network", () => {
  it("writes, other origins, other API paths and unknown files", async () => {
    const w = setup();
    expect(await w.fetch("/api/items/1/star", { method: "PUT" })).toBeUndefined();
    expect(await w.fetch("/api/items/mark-read", { method: "POST" })).toBeUndefined();
    expect(await w.fetch("https://elsewhere.test/api/bootstrap")).toBeUndefined();
    expect(await w.fetch("/api/events")).toBeUndefined();
    expect(await w.fetch("/api/auth/me")).toBeUndefined();
    expect(await w.fetch("/api/greader.php/reader/api/0/token")).toBeUndefined();
    expect(await w.fetch("/sw.js")).toBeUndefined();
    expect(await w.fetch("/api/items", { mode: "navigate" })).toBeUndefined();
  });
});

describe("navigation", () => {
  const shell = (w: ReturnType<typeof setup>) => w.stores.set("kipple-shell-0000000000001-aaaa", fakeCache());
  it("uses the network, and the cached shell when it is down or the server is failing", async () => {
    const w = setup(["/"]);
    shell(w);
    await w.stores.get("kipple-shell-0000000000001-aaaa")!.put("/", new Response("cached index"));
    w.setNetwork(() => new Response("live index"));
    expect(await (await w.fetch("/l/unread", { mode: "navigate" }))!.text()).toBe("live index");

    w.setNetwork(() => {
      throw new TypeError("offline");
    });
    expect(await (await w.fetch("/l/unread", { mode: "navigate" }))!.text()).toBe("cached index");

    w.setNetwork(() => new Response("bad gateway", { status: 502 }));
    expect(await (await w.fetch("/i/5", { mode: "navigate" }))!.text()).toBe("cached index");
  });

  it("a reload to sign in again waits for a slow network instead of serving the shell, so it reaches the login page", async () => {
    vi.useFakeTimers();
    try {
      const w = setup(["/"]);
      shell(w);
      await w.stores.get("kipple-shell-0000000000001-aaaa")!.put("/", new Response("cached index"));
      const slow = (body: string) => () => new Promise<Response>((resolve) => setTimeout(() => resolve(new Response(body)), 8000));

      // Without the marker a slow navigation gets the shell, which would only land in the expired sign-in again.
      w.setNetwork(slow("live index"));
      const plain = w.fetch("/l/unread", { mode: "navigate" });
      await vi.advanceTimersByTimeAsync(9000);
      expect(await (await plain)!.text()).toBe("cached index");

      w.setNetwork(slow("the access proxy's login page"));
      expect(swSource).toContain(`const SIGN_IN_RELOAD = "${SIGN_IN_RELOAD}";`); // the page and the worker agree
      const signIn = w.fetch(`/l/unread?${SIGN_IN_RELOAD}=1`, { mode: "navigate" });
      await vi.advanceTimersByTimeAsync(9000);
      expect(await (await signIn)!.text()).toBe("the access proxy's login page");

      // A network that is really down still gets the shell.
      w.setNetwork(() => {
        throw new TypeError("offline");
      });
      expect(await (await w.fetch(`/l/unread?${SIGN_IN_RELOAD}=2`, { mode: "navigate" }))!.text()).toBe("cached index");
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not answer navigations to the API or images with the app", async () => {
    const w = setup();
    expect(await w.fetch("/img/x", { mode: "navigate" })).toBeUndefined();
    expect(await w.fetch("/healthz", { mode: "navigate" })).toBeUndefined();
  });
});

describe("slow and failing networks", () => {
  it("with no copy kept, a slow answer is waited for, not turned into an error", async () => {
    vi.useFakeTimers();
    try {
      const w = setup();
      w.setNetwork(() => new Promise<Response>((resolve) => setTimeout(() => resolve(json({ slow: true })), 8000)));
      const pending = w.fetch("/api/items?view=all");
      await vi.advanceTimersByTimeAsync(9000);
      expect(await (await pending)!.json()).toEqual({ slow: true });
    } finally {
      vi.useRealTimers();
    }
  });

  it("with a copy kept, a slow answer gets the copy now and refreshes it when it lands", async () => {
    vi.useFakeTimers();
    try {
      const w = setup();
      w.setNetwork(() => json({ v: 1 }));
      await w.fetch("/api/bootstrap");
      w.setNetwork(() => new Promise<Response>((resolve) => setTimeout(() => resolve(json({ v: 2 })), 8000)));
      const pending = w.fetch("/api/bootstrap");
      await vi.advanceTimersByTimeAsync(9000);
      const res = (await pending)!;
      expect(res.headers.get("X-Kipple-Cache")).toBe("1");
      expect(await res.json()).toEqual({ v: 1 });
      w.setNetwork(() => {
        throw new TypeError("offline");
      });
      expect(await (await w.fetch("/api/bootstrap"))!.json()).toEqual({ v: 2 });
    } finally {
      vi.useRealTimers();
    }
  });

  it("searches being typed are not kept", async () => {
    const w = setup();
    w.setNetwork(() => json({ n: 1 }));
    await w.fetch("/api/items?view=all&q=ab&typing=1");
    w.setNetwork(() => {
      throw new TypeError("offline");
    });
    await expect(w.fetch("/api/items?view=all&q=ab&typing=1")).rejects.toThrow();
  });

  it("root files such as the manifest are network first", async () => {
    const w = setup(["/manifest.webmanifest"]);
    w.stores.set("kipple-shell-0000000000001-aaaa", fakeCache());
    await w.stores.get("kipple-shell-0000000000001-aaaa")!.put("/manifest.webmanifest", new Response("old"));
    w.setNetwork(() => new Response("new"));
    expect(await (await w.fetch("/manifest.webmanifest"))!.text()).toBe("new");
    w.setNetwork(() => {
      throw new TypeError("offline");
    });
    expect(await (await w.fetch("/manifest.webmanifest"))!.text()).toBe("old");
  });

  it("an image the cache cannot store still loads, and a partial response is not stored", async () => {
    const w = setup();
    w.setNetwork(() => new Response("png"));
    await w.fetch("/img/warm"); // creates the images cache
    w.stores.get("kipple-images")!.put = async () => {
      throw new Error("QuotaExceededError");
    };
    expect(await (await w.fetch("/img/other"))!.text()).toBe("png");
    w.setNetwork(() => new Response("partial", { status: 206 }));
    expect((await w.fetch("/img/range"))!.status).toBe(206);
  });
});

describe("read-only API answers", () => {
  it("network first; the last good answer, marked, when the network is down", async () => {
    const w = setup();
    w.setNetwork(() => json({ n: 1 }));
    expect(await (await w.fetch("/api/items?view=unread&limit=50"))!.json()).toEqual({ n: 1 });

    w.setNetwork(() => {
      throw new TypeError("offline");
    });
    const off = (await w.fetch("/api/items?view=unread&limit=50"))!;
    expect(off.headers.get("X-Kipple-Cache")).toBe("1");
    expect(await off.json()).toEqual({ n: 1 });
    await expect(w.fetch("/api/items?view=all")).rejects.toThrow("offline"); // nothing kept for it
  });

  it("a server error falls back to the copy; a 401 is the answer and is never kept", async () => {
    const w = setup();
    w.setNetwork(() => json({ ok: 1 }));
    await w.fetch("/api/bootstrap");
    w.setNetwork(() => json({ error: "internal" }, 503));
    expect((await w.fetch("/api/bootstrap"))!.headers.get("X-Kipple-Cache")).toBe("1");
    w.setNetwork(() => json({ error: "auth" }, 401));
    expect((await w.fetch("/api/items/9"))!.status).toBe(401);
    w.setNetwork(() => {
      throw new TypeError("offline");
    });
    await expect(w.fetch("/api/items/9")).rejects.toThrow();
  });

  it("include=content is stored as one entry per item and as the plain list", async () => {
    const w = setup();
    const items = [
      { id: "11", title: "A", content_html: "<p>a</p>" },
      { id: "12", title: "B", content_html: "<p>b</p>" },
    ];
    w.setNetwork(() => json({ items, next_cursor: null }));
    await w.fetch("/api/items?view=unread&limit=50&include=content");
    w.setNetwork(() => {
      throw new TypeError("offline");
    });
    expect(((await (await w.fetch("/api/items/12"))!.json()) as { content_html: string }).content_html).toBe("<p>b</p>");
    const list = (await (await w.fetch("/api/items?view=unread&limit=50"))!.json()) as { items: unknown[] };
    expect(list.items).toHaveLength(2);
  });

  it("clear-data drops the API copies and the images, and nothing else", async () => {
    const w = setup();
    w.setNetwork(() => json({ n: 1 }));
    await w.fetch("/api/bootstrap");
    w.setNetwork(() => new Response("png"));
    await w.fetch("/img/abc");
    w.stores.set("kipple-shell-0000000000001-aaaa", fakeCache());
    await w.message({ type: "clear-data" });
    expect([...w.stores.keys()]).toEqual(["kipple-shell-0000000000001-aaaa"]);
  });
});

describe("skip-waiting", () => {
  it("a waiting worker takes over when the page asks (the Reload button after a rebuild), and only then", async () => {
    const w = setup();
    await w.message({ type: "something-else" });
    expect(w.skipped()).toBe(0);
    await w.message({ type: "skip-waiting" });
    expect(w.skipped()).toBe(1);
  });
});

describe("clear-data while requests are still out", () => {
  async function lateAnswer(path: string, body: () => Response) {
    const w = setup([], undefined, { attached: true });
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    w.setNetwork(async () => {
      await held;
      return body();
    });
    const pending = w.fetch(path);
    await w.message({ type: "clear-data" }); // sign-out lands while the answer is on its way
    release();
    const res = (await pending)!;
    return { w, res };
  }
  const entries = async (w: ReturnType<typeof setup>, name: string) => ((await w.stores.get(name)?.keys()) ?? []).map((k) => k.url);

  it("an API answer from before the clear is returned but not kept", async () => {
    const { w, res } = await lateAnswer("/api/bootstrap", () => json({ user: "old session" }));
    expect(res.status).toBe(200);
    expect(await entries(w, "kipple-data")).toEqual([]);
  });

  it("a prefetched list with content from before the clear keeps no article", async () => {
    const { w } = await lateAnswer("/api/items?view=unread&include=content", () => json({ items: [{ id: "1", content_html: "<p>x</p>" }], next_cursor: null }));
    expect(await entries(w, "kipple-data")).toEqual([]);
  });

  it("an image from before the clear is not kept; one requested after it is", async () => {
    const { w } = await lateAnswer("/img/late", () => new Response("png"));
    expect(await entries(w, "kipple-images")).toEqual([]);
    w.setNetwork(() => new Response("png"));
    await w.fetch("/img/fresh");
    expect(await entries(w, "kipple-images")).toEqual([`${ORIGIN}/img/fresh`]);
  });
});

describe("images and assets", () => {
  it("images are cache first", async () => {
    const w = setup();
    let hits = 0;
    w.setNetwork(() => {
      hits++;
      return new Response("png");
    });
    await w.fetch("/img/abc");
    expect(await (await w.fetch("/img/abc"))!.text()).toBe("png");
    expect(hits).toBe(1);
    await w.fetch("/api/feeds/3/icon");
    await w.fetch("/api/feeds/3/icon");
    expect(hits).toBe(2);
  });

  it("assets come from any shell cache first, and are kept once fetched", async () => {
    const w = setup();
    w.stores.set("kipple-shell-0000000000001-old", fakeCache());
    await w.stores.get("kipple-shell-0000000000001-old")!.put("/assets/old.js", new Response("old chunk"));
    w.setNetwork(() => {
      throw new TypeError("offline");
    });
    expect(await (await w.fetch("/assets/old.js"))!.text()).toBe("old chunk");
    w.setNetwork(() => new Response("font"));
    await w.fetch("/assets/f.woff2");
    w.setNetwork(() => {
      throw new TypeError("offline");
    });
    expect(await (await w.fetch("/assets/f.woff2"))!.text()).toBe("font");
  });
});
