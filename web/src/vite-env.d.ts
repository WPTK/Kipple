/// <reference types="vite/client" />

declare module "virtual:kipple-themes.css";

declare module "virtual:kipple-whats-new" {
  const releases: import("./lib/whatsNewParse").Release[];
  export default releases;
}

/** The release this bundle was built for ("dev" outside a build) and this build's id (vite.config.ts). */
declare const __KIPPLE_VERSION__: string;
declare const __KIPPLE_BUILD__: string;
