import { useEffect, useRef, useState } from "react";
import { errorMessage } from "@/api/client";
import { useAbout } from "@/api/about";
import { aboutRows, debugText, readClientFacts, type ClientFacts } from "@/lib/aboutText";
import { BUNDLE } from "@/lib/buildInfo";
import { Button } from "@/ui/button";
import { Notice, Skeleton } from "@/ui/kit";
import { openWhatsNew } from "@/shell/WhatsNew";

type CopyState = "idle" | "copied" | "failed";

/** The keys that do something in Kipple. There are no single-key shortcuts, so every line is a key that moves, opens or closes. */
const KEYBOARD_KEYS: readonly { keys: readonly string[]; does: string }[] = [
  { keys: ["Tab", "Shift+Tab"], does: "Move between controls. A list of articles is one Tab stop; the arrow keys move within it." },
  { keys: ["Up arrow", "Down arrow"], does: "Move to the previous or next article in a list, the Digest or the Feed index." },
  { keys: ["Home", "End"], does: "Move to the first or last article." },
  { keys: ["Page up", "Page down"], does: "Move up or down a screenful of articles." },
  { keys: ["Enter", "Space"], does: "Open the article that has focus, or press the button that has it." },
  { keys: ["Right arrow", "Left arrow"], does: "In the sidebar, open or close the folder that has focus." },
  { keys: ["Esc"], does: "Close an open menu or dialog. With none open, leave the article you are reading and return to its list, or leave Search for the screen before it." },
];

function KeyboardList() {
  return (
    <section aria-labelledby="about-keyboard" className="flex flex-col gap-2">
      <h2 id="about-keyboard" className="text-sm font-semibold">
        Keyboard
      </h2>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
        {KEYBOARD_KEYS.map((k) => (
          <div key={k.keys.join("/")} className="contents">
            <dt className="flex flex-wrap items-start gap-1 text-fg2">
              {k.keys.map((key) => (
                <kbd key={key} className="rounded border border-line bg-surface px-1.5 py-0.5 font-sans text-xs text-fg">
                  {key}
                </kbd>
              ))}
            </dt>
            <dd className="min-w-0 break-words">{k.does}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

/**
 * Settings > About: the version and build of the server and of this page, and a "Copy debug info" button that
 * builds a plain-text block for a bug report, shows it, and copies it. The block holds no username, address or path.
 * Nothing on this screen contacts anything outside Kipple.
 */
export function AboutSection() {
  const about = useAbout();
  const [client, setClient] = useState<ClientFacts | null>(null);
  const [text, setText] = useState<string | null>(null);
  const [copy, setCopy] = useState<CopyState>("idle");
  const area = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    let live = true;
    void readClientFacts(BUNDLE).then((c) => live && setClient(c));
    return () => {
      live = false;
    };
  }, []);

  if (about.isPending || !client) return <Skeleton rows={6} label="Loading" />;
  if (about.isError) return <Notice tone="error">{`Couldn't load the version details: ${errorMessage(about.error)}`}</Notice>;

  const a = about.data;
  const rows = aboutRows(a, client);

  async function copyDebug() {
    const block = debugText(a, client!);
    setText(block); // shown first, so there is something to copy by hand if the browser refuses
    try {
      await navigator.clipboard.writeText(block);
      setCopy("copied");
    } catch {
      setCopy("failed");
      requestAnimationFrame(() => area.current?.select());
    }
  }

  return (
    <>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
        {rows.map((r) => (
          <div key={r.label} className="contents">
            <dt className="text-fg2">{r.label}</dt>
            <dd className="min-w-0 font-medium break-words">{r.value}</dd>
          </div>
        ))}
      </dl>
      <div className="flex flex-wrap gap-2">
        <Button variant="solid" onClick={() => void copyDebug()}>Copy debug info</Button>
        <Button onClick={openWhatsNew}>
          What's new
        </Button>
      </div>
      <p role="status" aria-live="polite" className="text-sm text-fg2">
        {copy === "copied" ? "Copied. Paste it into your bug report." : copy === "failed" ? "Your browser would not copy it. Select the text below and copy it." : ""}
      </p>
      {text !== null ? (
        <textarea
          ref={area}
          readOnly
          aria-label="Debug info"
          value={text}
          rows={Math.min(text.split("\n").length + 1, 22)}
          onFocus={(e) => e.currentTarget.select()}
          className="w-full rounded-lg border border-field bg-surface p-3 font-mono text-xs text-fg"
        />
      ) : null}
      <KeyboardList />
      <p className="text-sm text-fg2">
        Kipple never checks for updates and never contacts anyone about this screen. To see what a newer version is, look at its release notes.
      </p>
    </>
  );
}
