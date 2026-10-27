import { lazy } from "react";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { Lazy } from "./App";

const Never = lazy(() => new Promise<{ default: () => null }>(() => undefined));

function at(path: string, header: "stats" | "settings") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Lazy label="Loading x" header={header}>
        <Never />
      </Lazy>
    </MemoryRouter>,
  );
}

describe("Lazy screens", () => {
  it("shows the Stats heading while the code downloads", () => {
    at("/stats", "stats");
    expect(screen.getByRole("heading", { level: 1, name: "Stats" })).toBeTruthy();
    expect(screen.getByText("Loading x")).toBeTruthy();
  });
  it("shows the Settings heading on the list", () => {
    at("/settings", "settings");
    expect(screen.getByRole("heading", { level: 1, name: "Settings" })).toBeTruthy();
  });
  it("draws no heading inside a Settings page on a phone, where the page's own name and back button belong", () => {
    at("/settings/account", "settings");
    expect(screen.queryByRole("heading", { level: 1 })).toBeNull();
    expect(screen.getByText("Loading x")).toBeTruthy();
  });
});
