import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "virtual:kipple-themes.css";
import "./index.css";
import App, { makeQueryClient } from "./App";
import { initOffline } from "./lib/offline";
import { initPrefs } from "./lib/prefs";
import { stripSignInReload } from "./lib/reload";
import { initTheme } from "./theme/theme";

// Before the router reads the address: a "Reload to sign in again" leaves its marker in it (lib/reload.ts).
stripSignInReload();

// Apply per-device appearance before the first render (the boot script already
// set data-theme in <head>; this wires live updates and meta theme-color).
initTheme();
initPrefs();
const queryClient = makeQueryClient();
initOffline(queryClient);

createRoot(document.getElementById("root") as HTMLElement).render(
  <StrictMode>
    <App client={queryClient} />
  </StrictMode>,
);
