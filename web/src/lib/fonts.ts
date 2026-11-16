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
  /** For a font of the device: the family names (or font keywords) whose presence means the device has it. */
  probe?: readonly string[];
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
  { id: "new-york", label: "New York", group: "On this device", stack: `"New York", ui-serif${serif}`, probe: ["New York", "ui-serif"] },
  { id: "charter", label: "Charter", group: "On this device", stack: `Charter, "Bitstream Charter"${serif}`, probe: ["Charter", "Bitstream Charter"] },
  { id: "sf-pro", label: "SF Pro", group: "On this device", stack: `"SF Pro Text", -apple-system, system-ui${sans}`, probe: ["SF Pro Text", "-apple-system"] },
  { id: "sf-mono", label: "SF Mono", group: "On this device", stack: `"SF Mono", ui-monospace${mono}`, probe: ["SF Mono", "ui-monospace"] },
  { id: "georgia", label: "Georgia", group: "On this device", stack: `Georgia${serif}`, probe: ["Georgia"] },
  { id: "menlo", label: "Menlo", group: "On this device", stack: `Menlo${mono}`, probe: ["Menlo"] },
];

/** Says whether the device can draw a font family (or a font keyword such as ui-serif). */
export type FontProbe = (family: string) => boolean;

const SAMPLE = "mmmmmmmmmmlli1WwQq";
const KEYWORD = /^(ui-|-apple-system$)/;

/**
 * Measures the family against each generic family: a font the device lacks falls through to the generic and measures the
 * same, an installed one differs from at least one of them. Where the browser has no font API, or no canvas, the font is
 * taken as there (the choice then behaves as it did before the check).
 */
function measuredProbe(family: string): boolean {
  if (typeof document === "undefined" || !("fonts" in document)) return true;
  try {
    const ctx = document.createElement("canvas").getContext("2d");
    if (!ctx) return true;
    const width = (font: string) => {
      ctx.font = `64px ${font}`;
      return ctx.measureText(SAMPLE).width;
    };
    const name = KEYWORD.test(family) ? family : `"${family}"`;
    return ["monospace", "serif", "sans-serif"].some((generic) => width(`${name}, ${generic}`) !== width(generic));
  } catch {
    return true;
  }
}

let probeFamily: FontProbe = measuredProbe;
const answers = new Map<string, boolean>();

/** Swaps the check (tests give a fixed one); null restores the browser's. The remembered answers are dropped. */
export function setFontProbe(p: FontProbe | null): void {
  probeFamily = p ?? measuredProbe;
  answers.clear();
}

/** Whether the device has the font, so choosing it shows the font and not the fallback. Bundled fonts always are. Remembered per family. */
export function isFontInstalled(f: FontDef): boolean {
  if (!f.probe) return true;
  return f.probe.some((family) => {
    let known = answers.get(family);
    if (known === undefined) {
      known = probeFamily(family);
      answers.set(family, known);
    }
    return known;
  });
}

export type FontId = string;
const FONT_IDS = FONTS.map((f) => f.id);
export const isFontId = (v: unknown): v is FontId => typeof v === "string" && FONT_IDS.includes(v);
export const fontById = (id: string): FontDef => FONTS.find((f) => f.id === id) ?? (FONTS[0] as FontDef);
export const FONT_GROUPS: readonly FontGroup[] = ["Default", "Easy to read", "Serif", "Sans-serif", "Monospace", "On this device"];
