// Reading fonts. Bundled ones are self-hosted through @fontsource (see index.css); the "System" group
// is used only where the device has it (New York, SF Pro, SF Mono, Georgia, Menlo, and Charter, which
// ships with macOS and iOS: Butterick's Charter release is not vendored, see web/README.md).

export type FontGroup = "Default" | "Easy to read" | "Serif" | "Sans-serif" | "Monospace" | "On this device";

export interface FontDef {
  id: string;
  label: string;
  group: FontGroup;
  /** CSS font-family list, or null for the built-in default. */
  stack: string | null;
  /** The name the server's `ui.font_body` setting uses, when it has one. */
  server?: string;
}

const serif = ", Georgia, 'Times New Roman', serif";
const sans = ", ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif";
const mono = ", ui-monospace, 'SF Mono', Menlo, Consolas, monospace";

export const FONTS: readonly FontDef[] = [
  { id: "default", label: "Default", group: "Default", stack: null, server: "" },
  { id: "easy", label: "Atkinson Hyperlegible Next (easy to read)", group: "Easy to read", stack: `"Atkinson Hyperlegible Next", ui-sans-serif, system-ui, sans-serif`, server: "Atkinson Hyperlegible Next" },
  { id: "literata", label: "Literata", group: "Serif", stack: `"Literata Variable"${serif}`, server: "Literata" },
  { id: "vollkorn", label: "Vollkorn", group: "Serif", stack: `"Vollkorn Variable"${serif}`, server: "Vollkorn" },
  { id: "gentium", label: "Gentium Book Plus", group: "Serif", stack: `"Gentium Book Plus"${serif}`, server: "Gentium Book Plus" },
  { id: "source-serif", label: "Source Serif 4", group: "Serif", stack: `"Source Serif 4 Variable"${serif}`, server: "Source Serif 4" },
  { id: "arvo", label: "Arvo", group: "Serif", stack: `Arvo${serif}`, server: "Arvo" },
  { id: "inter", label: "Inter", group: "Sans-serif", stack: `"Inter Variable"${sans}`, server: "Inter" },
  { id: "manrope", label: "Manrope", group: "Sans-serif", stack: `"Manrope Variable"${sans}`, server: "Manrope" },
  { id: "source-sans", label: "Source Sans 3", group: "Sans-serif", stack: `"Source Sans 3 Variable"${sans}`, server: "Source Sans 3" },
  { id: "jetbrains-mono", label: "JetBrains Mono", group: "Monospace", stack: `"JetBrains Mono Variable"${mono}`, server: "JetBrains Mono" },
  { id: "source-code", label: "Source Code Pro", group: "Monospace", stack: `"Source Code Pro"${mono}`, server: "Source Code Pro" },
  { id: "new-york", label: "New York", group: "On this device", stack: `"New York", ui-serif${serif}`, server: "New York" },
  { id: "charter", label: "Charter", group: "On this device", stack: `Charter, "Bitstream Charter"${serif}`, server: "Charter" },
  { id: "sf-pro", label: "SF Pro", group: "On this device", stack: `"SF Pro Text", -apple-system, system-ui${sans}`, server: "SF Pro" },
  { id: "sf-mono", label: "SF Mono", group: "On this device", stack: `"SF Mono", ui-monospace${mono}`, server: "SF Mono" },
  { id: "georgia", label: "Georgia", group: "On this device", stack: `Georgia${serif}`, server: "Georgia" },
  { id: "menlo", label: "Menlo", group: "On this device", stack: `Menlo${mono}`, server: "Menlo" },
];

export type FontId = string;
export const FONT_IDS = FONTS.map((f) => f.id);
export const isFontId = (v: unknown): v is FontId => typeof v === "string" && FONT_IDS.includes(v);
export const fontById = (id: string): FontDef => FONTS.find((f) => f.id === id) ?? (FONTS[0] as FontDef);
export const FONT_GROUPS: readonly FontGroup[] = ["Default", "Easy to read", "Serif", "Sans-serif", "Monospace", "On this device"];
