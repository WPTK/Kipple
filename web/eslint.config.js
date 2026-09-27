import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist", "node_modules"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  reactHooks.configs.flat.recommended,
  {
    files: ["src/**/*.{ts,tsx}", "vite.config.ts"],
    languageOptions: { globals: { ...globals.browser } },
  },
  {
    files: ["sw/**/*.js"],
    languageOptions: { globals: { ...globals.serviceworker } },
  },
  {
    files: ["scripts/**/*.mjs", "vite.config.ts", "eslint.config.js"],
    languageOptions: { globals: { ...globals.node } },
  },
  {
    // The UAT runner is Node, but the probes it passes to page.evaluate run in the page.
    files: ["uat/**/*.mjs"],
    languageOptions: { globals: { ...globals.node, ...globals.browser } },
  },
  {
    rules: {
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_", varsIgnorePattern: "^_" }],
    },
  },
);
