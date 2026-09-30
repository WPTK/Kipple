/** What this bundle is, stamped in by vite.config.ts ("dev" outside a production build). */
export const BUNDLE = { version: __KIPPLE_VERSION__, build: __KIPPLE_BUILD__ };

/**
 * Whether the server was rebuilt since this page loaded: it reports a web build id and that id is not the one this
 * bundle carries. A development bundle never says so, and a server that reports none (an older one, or a build
 * without the tag) cannot be compared.
 */
export function serverRebuilt(serverBuild: string | undefined | null, bundleBuild: string = BUNDLE.build): boolean {
  return !!serverBuild && bundleBuild !== "dev" && serverBuild !== bundleBuild;
}

/**
 * Reload into the new build: ask a waiting service worker to take over first (it normally skips waiting on its own,
 * so this is the belt to that braces), then reload. Never throws and never waits long: a reload happens whatever the
 * worker does.
 */
export async function reloadForUpdate(
  reload: () => void = () => window.location.reload(),
  sw: Pick<ServiceWorkerContainer, "getRegistration"> | undefined = typeof navigator === "undefined" ? undefined : navigator.serviceWorker,
): Promise<void> {
  try {
    const reg = await Promise.race([sw?.getRegistration(), new Promise<undefined>((r) => setTimeout(r, 1000))]);
    reg?.waiting?.postMessage({ type: "skip-waiting" });
  } catch {
    // no worker to ask: the reload below is the whole job
  }
  reload();
}
