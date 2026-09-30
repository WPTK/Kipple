import { createHash } from "node:crypto";
import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { bootScript, themesCss } from "./src/theme/css.ts";
import { parseChangelog } from "./src/lib/whatsNewParse.ts";

// The release this bundle is built for (the Dockerfile passes VERSION to the web stage too) and an id for this
// particular build. The id is written into index.html as <meta name="kipple-build"> and into the bundle as
// __KIPPLE_BUILD__; the server reports the one in its embedded index.html as bootstrap.web_build, and a page
// whose own id differs knows the server was rebuilt since it loaded. It comes from the version and the build time
// (SOURCE_DATE_EPOCH when set, so a rebuild of the same commit gets the same id). "dev" outside a build.
const VERSION = process.env.VERSION?.trim() || "dev";
function buildId(): string {
  const stamp = String(Math.floor(Number(process.env.SOURCE_DATE_EPOCH) * 1000) || Date.now());
  return createHash("sha256").update(`${VERSION}:${stamp}`).digest("hex").slice(0, 10);
}

// The build id in index.html (a build only).
function kippleBuildMeta(id: string): Plugin {
  let isBuild = false;
  return {
    name: "kipple-build-meta",
    configResolved: (c) => {
      isBuild = c.command === "build";
    },
    transformIndexHtml: () => (isBuild ? [{ tag: "meta", attrs: { name: "kipple-build", content: id }, injectTo: "head" as const }] : []),
  };
}

// The newest ten releases of CHANGELOG.md as a virtual module: a lazy chunk with a hashed name, loaded only when
// "What's new" opens. The Docker web stage copies the changelog to ../CHANGELOG.md; a checkout without it gets [].
function kippleWhatsNew(): Plugin {
  const id = "virtual:kipple-whats-new";
  const file = fileURLToPath(new URL("../CHANGELOG.md", import.meta.url));
  return {
    name: "kipple-whats-new",
    resolveId: (s) => (s === id ? "\0" + id : null),
    load(s) {
      if (s !== "\0" + id) return null;
      this.addWatchFile(file);
      let text = "";
      try {
        text = readFileSync(file, "utf8");
      } catch {
        // no changelog next to the app: the panel simply has nothing to show
      }
      return `export default ${JSON.stringify(parseChangelog(text, 10))};`;
    },
  };
}

// Every scheme's CSS variables, as a virtual stylesheet.
function kippleThemes(): Plugin {
  const id = "virtual:kipple-themes.css";
  return {
    name: "kipple-themes",
    resolveId: (s) => (s === id ? "\0" + id : null),
    load: (s) => (s === "\0" + id ? themesCss() : null),
  };
}

// Theme boot script. In dev it is inlined. In a build it is emitted as a
// hashed file under /assets/ and loaded by a blocking classic <script src> in
// <head>, so a strict `script-src 'self'` CSP works without an inline hash and
// internal/web (which only serves /assets/* from dist) can find it.
function themeBoot(): Plugin {
  let isBuild = false;
  const code = bootScript();
  const file = `assets/theme-boot-${createHash("sha256").update(code).digest("hex").slice(0, 8)}.js`;
  return {
    name: "kipple-theme-boot",
    configResolved: (c) => {
      isBuild = c.command === "build";
    },
    generateBundle() {
      this.emitFile({ type: "asset", fileName: file, source: code });
    },
    transformIndexHtml: () => [
      isBuild
        ? { tag: "script", attrs: { src: `/${file}` }, injectTo: "head" }
        : { tag: "script", children: code, injectTo: "head" },
    ],
  };
}

// vite empties dist/ on every build; web/embed.go needs the committed .gitkeep
// to keep existing so a fresh clone still compiles.
function keepGitkeep(): Plugin {
  let outDir = "dist";
  return {
    name: "kipple-keep-gitkeep",
    apply: "build",
    configResolved: (c) => {
      outDir = c.build.outDir;
    },
    closeBundle() {
      writeFileSync(new URL(`./${outDir}/.gitkeep`, import.meta.url), "");
    },
  };
}

// The service worker: sw/sw.js with the build id and the precache list filled in, written to dist/sw.js
// after everything else exists. It precaches the shell (index, JS, CSS, root icons) but not fonts or images,
// which it keeps as they are used, so the first load stays small. The id starts with the build time so the
// worker can tell which shell caches are the newest.
function kippleSw(): Plugin {
  let outDir = "dist";
  return {
    name: "kipple-sw",
    apply: "build",
    configResolved: (c) => {
      outDir = c.build.outDir;
    },
    closeBundle() {
      const root = fileURLToPath(new URL(`./${outDir}/`, import.meta.url));
      const list = ["/"];
      for (const f of readdirSync(root).sort()) {
        if (statSync(root + f).isFile() && f !== "index.html" && f !== "sw.js" && !f.startsWith(".")) list.push(`/${f}`);
      }
      for (const f of readdirSync(root + "assets").sort()) {
        if (/\.(js|css)$/.test(f)) list.push(`/assets/${f}`);
      }
      const id = createHash("sha256").update(list.join(",")).update(readFileSync(root + "index.html")).digest("hex").slice(0, 10);
      const stamp = String(Math.floor(Number(process.env.SOURCE_DATE_EPOCH) * 1000) || Date.now()).padStart(13, "0");
      const src = readFileSync(fileURLToPath(new URL("./sw/sw.js", import.meta.url)), "utf8")
        .replace('/*BUILD*/ "dev"', JSON.stringify(`${stamp}-${id}`))
        .replace("/*PRECACHE*/ []", JSON.stringify(list));
      writeFileSync(root + "sw.js", src);
    },
  };
}

// KIPPLE_DEV_BACKEND points the dev proxy at another local server (a second instance, a worktree build).
const backend = process.env.KIPPLE_DEV_BACKEND ?? "http://127.0.0.1:7080";

export default defineConfig(({ command }) => {
  const build = command === "build" ? buildId() : "dev";
  return {
    plugins: [react(), tailwindcss(), kippleThemes(), themeBoot(), kippleBuildMeta(build), kippleWhatsNew(), keepGitkeep(), kippleSw()],
    define: { __KIPPLE_VERSION__: JSON.stringify(command === "build" ? VERSION : "dev"), __KIPPLE_BUILD__: JSON.stringify(build) },
    resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
    server: {
      host: "127.0.0.1",
      proxy: {
        "/api": backend,
        "/img": backend,
        "/healthz": backend,
      },
    },
    // Nothing is inlined as a data: URI: the page policy is font-src 'self', and small font subsets were being blocked.
    build: { target: "es2022", assetsInlineLimit: 0 },
    test: {
      environment: "jsdom",
      setupFiles: ["./src/test/setup.ts"],
      css: false,
      restoreMocks: true,
      // The screens are lazy chunks and jsdom renders large trees; on a busy machine the default 5 s is too tight.
      testTimeout: 20_000,
      // Visibility only (SQA plan): reported in CI logs, not a gate. No thresholds are enforced.
      coverage: {
        provider: "v8",
        reporter: ["text-summary", "lcov"],
        include: ["src/**/*.{ts,tsx}"],
        exclude: ["src/test/**", "src/**/*.d.ts", "src/main.tsx"],
      },
    },
  };
});
