import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { WhatsNewDialog } from "./WhatsNew";

const releases = [{ version: "1.0.0", date: "2026-01-01", intro: "", groups: [{ kind: "Added", items: ["A thing."] }] }];

function Page({ heading }: { heading: boolean }) {
  const [open, setOpen] = useState(true);
  return (
    <main id="main" tabIndex={-1}>
      {heading ? (
        <h1 data-route-heading tabIndex={-1}>
          Unread
        </h1>
      ) : null}
      {open ? <WhatsNewDialog releases={releases} onClose={() => setOpen(false)} /> : null}
    </main>
  );
}

describe("WhatsNewDialog", () => {
  it("gives focus to the screen's heading when it closes with nothing focused before it", async () => {
    render(<Page heading />);
    await screen.findByRole("dialog");
    await userEvent.setup().keyboard("{Escape}");
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Unread" })));
  });

  it("falls back to the main region when the screen has no heading", async () => {
    render(<Page heading={false} />);
    await screen.findByRole("dialog");
    await userEvent.setup().keyboard("{Escape}");
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("main")));
  });
});
