import { useEffect, useRef, useState } from "react";
import { errorMessage } from "@/api/client";
import { useAbout } from "@/api/about";
import { aboutRows, debugText, readClientFacts, type ClientFacts } from "@/lib/aboutText";
import { BUNDLE } from "@/lib/buildInfo";
import { Button } from "@/ui/button";
import { Notice, Skeleton } from "@/ui/kit";
import { openWhatsNew } from "@/shell/WhatsNew";

type CopyState = "idle" | "copied" | "failed";

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
          className="w-full rounded-lg border border-line bg-surface p-3 font-mono text-xs text-fg"
        />
      ) : null}
      <p className="text-sm text-fg2">
        Kipple never checks for updates and never contacts anyone about this screen. To see what a newer version is, look at its release notes.
      </p>
    </>
  );
}
