import { describe, expect, it } from "vitest";
import swSource from "../sw/sw.js?raw";

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

function setup(precache: string[] = [], build = "0000000000001-aaaa") {
  const stores = new Map<string, ReturnType<typeof fakeCache>>();
  const open = (name: string) => {
    if (!stores.has(name)) stores.set(name, fakeCache());
    return stores.get(name)!;
  };
  const caches = {
    open: async (n: string) => open(n),
    keys: async () => [...stores.keys()],
    delete: async (n: string) => stores.delete(n),
    match: async (req: { url: string }) => {
      for (const c of stores.values()) {
        const hit = await c.match(req);
        if (hit) return hit;
      }
      return undefined;
    },
  };
  const handlers = new Map<string, Handler>();
  const self = {
    location: { origin: ORIGIN },
    addEventListener: (t: string, f: Handler) => handlers.set(t, f),
    skipWaiting: async () => {},
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

  it("does not answer navigations to the API or images with the app", async () => {
    const w = setup();
    expect(await w.fetch("/img/x", { mode: "navigate" })).toBeUndefined();
    expect(await w.fetch("/healthz", { mode: "navigate" })).toBeUndefined();
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
