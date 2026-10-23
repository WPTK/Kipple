import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";

// [regex source, message] for a Tailwind arbitrary value written in a class string.
const DESIGN_VALUES = [
  [String.raw`(^|[\s:!-])(bg|text|border|fill|stroke|ring|outline|shadow|from|via|to|decoration|accent|caret|divide)-(\[(#|rgb|hsl|oklch|color:)|black\b|white\b)`, "Use a scheme token (bg-bg, text-fg, border-field, bg-scrim ...), not a literal colour. New tokens go in src/theme/schemes.json."],
  [String.raw`(^|[\s:!-])text-\[[0-9.]+(px|rem|em)\]`, "Use the type scale (text-xs, text-sm ... text-micro in src/index.css), which follows the text size setting."],
  [String.raw`(^|[\s:!-])-?(p|px|py|pt|pb|pl|pr|m|mx|my|mt|mb|ml|mr|gap|gap-x|gap-y|space-x|space-y|inset|top|right|bottom|left)-\[[0-9.]+(px|rem)\]`, "Use a step of the spacing scale (p-3, gap-2.5 ...), not an arbitrary length."],
  [String.raw`(^|[\s:!-])(min|max)-\[(?!(380|420|520|560|640)px\]|20rem\])[0-9.]+(px|rem)\]:`, "Use an existing breakpoint (380, 420, 520, 560 or 640 px, 20rem); the three-pane widths come from the pane minimums."],
];

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
    files: ["uat/wizard.mjs", "uat/offline.mjs", "uat/keyboard.mjs", "uat/folders.mjs", "uat/downloads.mjs", "uat/focus.mjs", "uat/keys.mjs", "uat/announce.mjs", "uat/controls.mjs", "uat/stats.mjs", "uat/stats-tiles.mjs", "uat/gazette-order.mjs", "uat/gazette-menu.mjs", "uat/group-by.mjs", "uat/kit.mjs"],
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
        // Design tokens, not one-off values: a class string never spells a colour, a type size, a spacing step or a
        // breakpoint the scale does not have (src/index.css and src/theme/schemes.json define them). The Gazette
        // layout is exempt until its masthead and section heads move onto the scale.
        ...DESIGN_VALUES.map(([pattern, message]) => ({ selector: `:matches(Literal[value=/${pattern}/], TemplateElement[value.raw=/${pattern}/])`, message })),
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
    // The Gazette keeps its own display sizes and rules until its masthead and section heads follow the type scale.
    files: ["src/layouts/gazette.tsx", "src/layouts/gazette.test.tsx"],
    rules: { "no-restricted-syntax": "off" },
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
