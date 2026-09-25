import { createHash } from "node:crypto";
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { bootScript, themesCss } from "./src/theme/css.ts";

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

const backend = "http://127.0.0.1:7080";

export default defineConfig({
  plugins: [react(), tailwindcss(), kippleThemes(), themeBoot(), keepGitkeep()],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  server: {
    host: "127.0.0.1",
    proxy: {
      "/api": backend,
      "/img": backend,
      "/healthz": backend,
    },
  },
  build: { target: "es2022" },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    css: false,
    restoreMocks: true,
  },
});
