import { act } from "@testing-library/react";

let navCount = 0;

/** An in-app navigation (a sidebar link or Back): the router hears a popstate, with `state` as the entry's state. */
export function navigateTo(path: string, state: unknown = null): void {
  navCount += 1;
  act(() => {
    window.history.pushState({ idx: navCount, usr: state }, "", path);
    window.dispatchEvent(new PopStateEvent("popstate", { state: window.history.state }));
  });
}

