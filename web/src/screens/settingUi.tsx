import { useEffect, useRef, useState } from "react";
import { Check } from "lucide-react";

/** How long "Saved" stays up. */
const SAVED_MARK_MS = 4000;

/**
 * The one confirmation for a setting that saved: a visible "Saved" beside the control for a few seconds. Flash it from
 * the success of the write; it is the same for a switch, a choice, a number and a text box.
 */
export function useSavedMark(): { saved: boolean; flash: () => void; hide: () => void } {
  const [saved, setSaved] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  return {
    saved,
    flash: () => {
      clearTimeout(timer.current);
      setSaved(true);
      timer.current = setTimeout(() => setSaved(false), SAVED_MARK_MS);
    },
    hide: () => {
      clearTimeout(timer.current);
      setSaved(false);
    },
  };
}

/** The "Saved" text. The status region is always in the page so a screen reader announces the text when it appears. */
export function SavedMark({ show }: { show: boolean }) {
  return (
    <span role="status" className="inline-flex min-h-6 items-center gap-1 text-sm text-fg2">
      {show ? (
        <>
          <Check aria-hidden="true" className="size-4" />
          Saved
        </>
      ) : null}
    </span>
  );
}

/** The first sentence of a help text and the rest of it. */
function splitHelp(text: string): [string, string] {
  const m = /^(.+?[.!?])\s+(?=[A-Z"])([\s\S]*)$/.exec(text);
  return m ? [m[1]!, m[2]!] : [text, ""];
}

/**
 * One sentence of help, with the exceptions and rules after it behind "More". The sentences come from the server's
 * description of the setting, so a description puts what every reader needs first.
 */
export function HelpText({ text, label }: { text: string; label: string }) {
  const [first, rest] = splitHelp(text);
  const [open, setOpen] = useState(false);
  return (
    <>
      {first}
      {open ? ` ${rest}` : null}
      {rest ? (
        <>
          {" "}
          <button type="button" aria-expanded={open} aria-label={`${open ? "Less" : "More"} about ${label}`} onClick={() => setOpen((o) => !o)} className="hit-pad text-link underline underline-offset-2">
            {open ? "Less" : "More"}
          </button>
        </>
      ) : null}
    </>
  );
}
