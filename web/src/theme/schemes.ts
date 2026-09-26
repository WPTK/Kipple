import data from "./schemes.json" with { type: "json" };

export type SchemeKind = "light" | "dark";
export type SchemeGroup = "light" | "color" | "dark" | "accessibility";

export interface Tokens {
  bg: string;
  surface: string;
  text: string;
  text2: string;
  border: string;
  accent: string;
  link: string;
  unread: string;
  star: string;
  danger: string;
  selection: string;
  /** Value for <meta name="theme-color">. */
  meta: string;
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
export const SCHEME_IDS = SCHEMES.map((s) => s.id);

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
  "accent",
  "link",
  "unread",
  "star",
  "danger",
  "selection",
  "meta",
] as const satisfies readonly (keyof Tokens)[];
