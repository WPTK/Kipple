// Build-time generators (used by vite.config.ts): the CSS variable sets for
// every scheme and the tiny boot script that prevents a flash of the wrong
// theme. Kept dependency-free so the Vite config can import it.
import { SCHEMES, TOKEN_KEYS } from "./schemes.ts";
import { CLOCK_TIME_PATTERN, DEFAULT_THEME_SETTINGS, THEME_STORAGE_KEY } from "./settings.ts";

export function themesCss(): string {
  return SCHEMES.map((s) => {
    const vars = TOKEN_KEYS.map((k) => `  --kp-${k}: ${s.tokens[k]};`).join("\n");
    return `:root[data-theme="${s.id}"] {\n  color-scheme: ${s.kind};\n${vars}\n}`;
  }).join("\n");
}

/**
 * Plain ES5, runs synchronously in <head> before first paint. It reads the
 * per-device stored choice, resolves follow-system against the OS setting (or the
 * schedule against the local clock) and
 * sets data-theme plus a single <meta name="theme-color">. It must stay in
 * step with resolveTheme() in settings.ts; a test evaluates it and compares.
 */
export function bootScript(): string {
  const meta: Record<string, string> = {};
  for (const s of SCHEMES) meta[s.id] = s.tokens.meta;
  return (
    "(function(){try{var M=" +
    JSON.stringify(meta) +
    ";var s=" +
    JSON.stringify(DEFAULT_THEME_SETTINGS) +
    ";var T=new RegExp(" +
    JSON.stringify(CLOCK_TIME_PATTERN) +
    ");" +
    "try{var r=JSON.parse(localStorage.getItem(" +
    JSON.stringify(THEME_STORAGE_KEY) +
    ')||"null");if(r){s.mode=r.mode==="fixed"||r.mode==="schedule"?r.mode:"follow";' +
    '["fixed","day","night"].forEach(function(k){if(typeof r[k]==="string"&&M[r[k]])s[k]=r[k]});' +
    '["nightStart","dayStart"].forEach(function(k){if(typeof r[k]==="string"&&T.test(r[k]))s[k]=r[k]})}}catch(e){}' +
    'var d=false;try{d=window.matchMedia("(prefers-color-scheme: dark)").matches}catch(e){}' +
    // The schedule: the same rule as isNightAt() in settings.ts, on the device's local clock.
    "var hm=function(t){return Number(t.slice(0,2))*60+Number(t.slice(3,5))};" +
    'if(s.mode==="schedule"){var o=new Date(),c=o.getHours()*60+o.getMinutes(),a=hm(s.nightStart),b=hm(s.dayStart);' +
    "d=a<b?(c>=a&&c<b):(a>b?(c>=a||c<b):false)}" +
    'var id=s.mode==="fixed"?s.fixed:(d?s.night:s.day);' +
    'document.documentElement.setAttribute("data-theme",id);' +
    "var old=document.querySelectorAll('meta[name=\"theme-color\"]');" +
    "for(var i=0;i<old.length;i++)old[i].parentNode.removeChild(old[i]);" +
    'var m=document.createElement("meta");m.name="theme-color";m.content=M[id];document.head.appendChild(m)}catch(e){}})();'
  );
}
