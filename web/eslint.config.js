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
    // The UAT runner is Node; the probes it hands to page.evaluate run in the page and only see the page.
    files: ["uat/run.mjs", "uat/measure-unload-refusal.mjs"],
    languageOptions: { globals: { ...globals.node } },
  },
  {
    // Node, with functions that run in the page (page.evaluate) written inline.
    files: ["uat/wizard.mjs", "uat/offline.mjs", "uat/keyboard.mjs", "uat/folders.mjs", "uat/downloads.mjs", "uat/focus.mjs", "uat/keys.mjs", "uat/announce.mjs", "uat/controls.mjs", "uat/stats.mjs", "uat/stats-tiles.mjs", "uat/gazette-order.mjs", "uat/gazette-menu.mjs", "uat/gazette-place.mjs", "uat/group-by.mjs", "uat/item-actions.mjs"],
    languageOptions: { globals: { ...globals.node, ...globals.browser } },
  },
  {
    files: ["uat/probes.mjs"],
    languageOptions: { globals: { ...globals.browser } },
  },
  {
    // The single-purpose flows: Node, with the functions they hand to page.evaluate written inline.
    files: ["uat/common.mjs", "uat/chrome.mjs", "uat/phone-shell.mjs", "uat/phone-header.mjs", "uat/phone-tabs.mjs", "uat/wrapping.mjs", "uat/phone-chrome.mjs", "uat/touch-menus.mjs", "uat/scroll-restore.mjs", "uat/rotate.mjs", "uat/feeds.mjs", "uat/states.mjs", "uat/wait-ready.mjs", "uat/all.mjs", "uat/row-name.mjs", "uat/opml-report.mjs"],
    languageOptions: { globals: { ...globals.node, ...globals.browser } },
  },
  {
    // A ref refreshed in a passive effect is stale until the browser has run the effect, which React may defer past
    // the next key press or pointer event. useLatestRef refreshes in a layout effect. Flags a useEffect whose callback
    // does nothing but assign to `x.current`, however it is spelled (React.useEffect, expression body, any number of
    // assignments, with or without a dependency list).
    files: ["src/**/*.{ts,tsx}"],
    rules: {
      "no-restricted-syntax": [
        "error",
        ...[
          "CallExpression[callee.name='useEffect']",
          "CallExpression[callee.type='MemberExpression'][callee.property.name='useEffect']",
        ].flatMap((call) => {
          const refWrite = "ExpressionStatement[expression.type='AssignmentExpression'][expression.left.type='MemberExpression'][expression.left.property.name='current']";
          const message =
            "A ref refreshed in a passive useEffect is stale until the effect runs. Use useLatestRef (src/lib/useLatestRef.ts), which refreshes in a layout effect.";
          return [
            // Expression body: useEffect(() => (ref.current = x))
            { selector: `${call} > :function[body.type='AssignmentExpression'][body.left.property.name='current']`, message },
            // Block body whose every statement is a write to `.current`
            {
              selector: `${call} > :function > BlockStatement[body.length>0]:not(:has(> :not(${refWrite})))`,
              message,
            },
          ];
        }),
      ],
    },
  },
  {
    // Every dropdown menu is built from ui/menu: a popover with a mouse, a bottom sheet with a finger, and no nested
    // menus (Sub), which neither is built for. So Radix's DropdownMenu is reached only through it.
    files: ["src/**/*.{ts,tsx}"],
    ignores: ["src/ui/menu.tsx"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          paths: [
            { name: "radix-ui", importNames: ["DropdownMenu"], message: "Import DropdownMenu from @/ui/menu." },
            { name: "@radix-ui/react-dropdown-menu", message: "Import DropdownMenu from @/ui/menu." },
          ],
        },
      ],
    },
  },
  {
    rules: {
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_", varsIgnorePattern: "^_" }],
    },
  },
);
