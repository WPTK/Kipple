import { useEffect, useRef, useState, type RefObject } from "react";
import { Headphones, Pause, Play, SkipBack, SkipForward, Square } from "lucide-react";
import { RATES, prefsStore, updatePrefs } from "@/lib/prefs";
import { Narrator, chunksOf, narratorStore, speechSupported } from "@/lib/speech";
import { useStore } from "@/lib/store";
import { Button } from "@/ui/button";
import { announce } from "@/shell/toasts";

/**
 * Listen controls for the open article: play, pause, stop, previous and next paragraph, and speed. Hidden when
 * the browser has no speech synthesis or the Accessibility setting "Listen to articles" is off. The article
 * stops when you leave it.
 */
export function ListenBar({ bodyRef, articleId }: { bodyRef: RefObject<HTMLElement | null>; articleId: string }) {
  const prefs = useStore(prefsStore);
  const st = useStore(narratorStore);
  const [n] = useState(() => new Narrator());
  const lastRate = useRef(prefs.rate);

  useEffect(() => {
    n.setOptions({ rate: prefs.rate, voiceURI: prefs.voice });
    if (lastRate.current !== prefs.rate) {
      lastRate.current = prefs.rate;
      n.refresh();
    }
  }, [prefs.rate, prefs.voice, n]);

  useEffect(() => () => n.stop(), [articleId, n]);

  if (!prefs.listen || !speechSupported()) return null;
  const idle = st.status === "idle";

  const play = () => {
    const root = bodyRef.current;
    if (!root) return;
    const chunks = chunksOf(root);
    if (chunks.length === 0) return announce("Nothing to read aloud in this article");
    n.start(chunks);
    announce("Reading aloud");
  };

  return (
    <div role="group" aria-label="Listen to this article" className="mx-auto mb-4 flex max-w-[min(var(--kp-measure),46rem)] flex-col gap-2 rounded-xl border border-line bg-surface px-3 py-2">
      <div className="flex flex-wrap items-center gap-1">
        {idle ? (
          <Button onClick={play}>
            <Headphones aria-hidden="true" />
            Listen
          </Button>
        ) : (
          <>
            {st.status === "playing" ? (
              <Button onClick={() => n.pause()}>
                <Pause aria-hidden="true" />
                Pause
              </Button>
            ) : (
              <Button onClick={() => n.resume()}>
                <Play aria-hidden="true" />
                Resume
              </Button>
            )}
            <Button variant="ghost" size="icon" aria-label="Previous paragraph" disabled={st.index === 0} onClick={() => n.skip(-1)}>
              <SkipBack aria-hidden="true" />
            </Button>
            <Button variant="ghost" size="icon" aria-label="Next paragraph" disabled={st.index >= st.total - 1} onClick={() => n.skip(1)}>
              <SkipForward aria-hidden="true" />
            </Button>
            <Button variant="ghost" onClick={() => n.stop()}>
              <Square aria-hidden="true" />
              Stop
            </Button>
            <span className="text-xs text-fg2" aria-hidden="true">
              {st.index + 1} of {st.total}
            </span>
          </>
        )}
        <label className="ml-auto flex items-center gap-2 text-sm">
          Speed
          <select
            value={prefs.rate}
            onChange={(e) => updatePrefs({ rate: Number(e.target.value) })}
            className="min-h-11 rounded-lg border border-line bg-bg px-2 text-fg"
          >
            {RATES.map((r) => (
              <option key={r} value={r}>
                {r}x
              </option>
            ))}
          </select>
        </label>
      </div>
      {idle ? (
        <p className="text-xs text-fg2">Uses your device's voices. On iPhone, reading stops if the screen locks or you leave the app.</p>
      ) : null}
    </div>
  );
}
