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
}

const serif = ", Georgia, 'Times New Roman', serif";
const sans = ", ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif";
const mono = ", ui-monospace, 'SF Mono', Menlo, Consolas, monospace";

export const FONTS: readonly FontDef[] = [
  { id: "default", label: "Default", group: "Default", stack: null },
  { id: "easy", label: "Atkinson Hyperlegible Next (easy to read)", group: "Easy to read", stack: `"Atkinson Hyperlegible Next", ui-sans-serif, system-ui, sans-serif` },
  { id: "literata", label: "Literata", group: "Serif", stack: `"Literata Variable"${serif}` },
  { id: "vollkorn", label: "Vollkorn", group: "Serif", stack: `"Vollkorn Variable"${serif}` },
  { id: "gentium", label: "Gentium Book Plus", group: "Serif", stack: `"Gentium Book Plus"${serif}` },
  { id: "source-serif", label: "Source Serif 4", group: "Serif", stack: `"Source Serif 4 Variable"${serif}` },
  { id: "arvo", label: "Arvo", group: "Serif", stack: `Arvo${serif}` },
  { id: "inter", label: "Inter", group: "Sans-serif", stack: `"Inter Variable"${sans}` },
  { id: "manrope", label: "Manrope", group: "Sans-serif", stack: `"Manrope Variable"${sans}` },
  { id: "source-sans", label: "Source Sans 3", group: "Sans-serif", stack: `"Source Sans 3 Variable"${sans}` },
  { id: "jetbrains-mono", label: "JetBrains Mono", group: "Monospace", stack: `"JetBrains Mono Variable"${mono}` },
  { id: "source-code", label: "Source Code Pro", group: "Monospace", stack: `"Source Code Pro"${mono}` },
  { id: "new-york", label: "New York", group: "On this device", stack: `"New York", ui-serif${serif}` },
  { id: "charter", label: "Charter", group: "On this device", stack: `Charter, "Bitstream Charter"${serif}` },
  { id: "sf-pro", label: "SF Pro", group: "On this device", stack: `"SF Pro Text", -apple-system, system-ui${sans}` },
  { id: "sf-mono", label: "SF Mono", group: "On this device", stack: `"SF Mono", ui-monospace${mono}` },
  { id: "georgia", label: "Georgia", group: "On this device", stack: `Georgia${serif}` },
  { id: "menlo", label: "Menlo", group: "On this device", stack: `Menlo${mono}` },
];

export type FontId = string;
export const FONT_IDS = FONTS.map((f) => f.id);
export const isFontId = (v: unknown): v is FontId => typeof v === "string" && FONT_IDS.includes(v);
export const fontById = (id: string): FontDef => FONTS.find((f) => f.id === id) ?? (FONTS[0] as FontDef);
export const FONT_GROUPS: readonly FontGroup[] = ["Default", "Easy to read", "Serif", "Sans-serif", "Monospace", "On this device"];
