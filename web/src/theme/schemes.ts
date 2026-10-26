import data from "./schemes.json" with { type: "json" };

type SchemeKind = "light" | "dark";
type SchemeGroup = "light" | "color" | "dark" | "accessibility";

export interface Tokens {
  bg: string;
  surface: string;
  text: string;
  text2: string;
  border: string;
  /** The boundary of a control that takes input (a field, a select, a switch, a segmented control): 3:1 against the page and the surface. `border` is for decoration. */
  field: string;
  accent: string;
  link: string;
  unread: string;
  star: string;
  danger: string;
  selection: string;
  /** Value for <meta name="theme-color">. */
  meta: string;
  /** The shadow a floating surface (sheet, menu, toast, tab bar) casts, as a CSS color. */
  shadow: string;
  /** The veil behind a modal sheet, as a CSS color. */
  scrim: string;
}

export interface Scheme {
  id: string;
  name: string;
  kind: SchemeKind;
  group: SchemeGroup;
  /** Shown in the short default theme list; the rest sit under "More themes". */
  featured: boolean;
  note?: string;
  tokens: Tokens;
}

export const SCHEMES = data as Scheme[];
const SCHEME_IDS = SCHEMES.map((s) => s.id);

export const DEFAULT_DAY = "paper";
export const DEFAULT_NIGHT = "midnight";

export function schemeById(id: string): Scheme {
  return SCHEMES.find((s) => s.id === id) ?? (SCHEMES.find((s) => s.id === DEFAULT_DAY) as Scheme);
}

export function isSchemeId(id: unknown): id is string {
  return typeof id === "string" && SCHEME_IDS.includes(id);
}

export const TOKEN_KEYS = [
  "bg",
  "surface",
  "text",
  "text2",
  "border",
  "field",
  "accent",
  "link",
  "unread",
  "star",
  "danger",
  "selection",
  "meta",
  "shadow",
  "scrim",
] as const satisfies readonly (keyof Tokens)[];
