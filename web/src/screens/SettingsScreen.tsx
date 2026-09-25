import { useId, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, authStore, errorMessage } from "@/api/client";
import { useBootstrap } from "@/api/queries";
import { STEPS, STEP_LABELS, TEXT_SIZES, TEXT_SIZE_LABELS, prefsStore, updatePrefs, type FontId, type Step } from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { ThemePicker } from "@/theme/ThemePicker";
import { Segmented } from "@/ui/segmented";
import { Button } from "@/ui/button";
import { toast } from "@/shell/toasts";

const stepOptions = STEPS.map((s) => ({ value: s, label: STEP_LABELS[s] }));

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section aria-labelledby={`sec-${title}`} className="border-b border-line px-4 py-5">
      <h2 id={`sec-${title}`} className="mb-3 text-lg font-bold">
        {title}
      </h2>
      <div className="flex flex-col gap-5">{children}</div>
    </section>
  );
}

/** Live sample of the chosen steps: a list row and a paragraph, scoped by data attributes. */
function DensityPreview({ list, reading }: { list: Step; reading: Step }) {
  return (
    <div aria-hidden="true" className="overflow-hidden rounded-xl border border-line bg-bg" data-testid="density-preview">
      <div data-list-density={list} className="flex min-h-[var(--row-min)] gap-3 border-b border-line px-4 py-[var(--row-py)]">
        <div className="flex min-w-0 flex-1 flex-col gap-[var(--row-gap)]">
          <div className="text-xs text-fg2">Example feed · 2h</div>
          <div className="clamp-title text-base leading-snug font-bold">A sample headline that may run onto a second or third line</div>
          <div className="clamp-snippet text-sm leading-snug text-fg2">
            The excerpt shows the first words of the article, so you can decide whether to open it, and grows with the density step.
          </div>
        </div>
        <div className="size-[var(--thumb)] shrink-0 rounded-lg bg-surface" />
      </div>
      <div data-reading-density={reading} className="article-body px-4 py-3 text-base">
        <p>Reading text uses this spacing between lines and paragraphs.</p>
        <p>A second paragraph shows the gap between them.</p>
      </div>
    </div>
  );
}

export function SettingsScreen() {
  const p = useStore(prefsStore);
  const boot = useBootstrap();
  const qc = useQueryClient();
  const switchId = useId();

  const signOut = async () => {
    try {
      await api("/api/auth/logout", { method: "POST" });
    } catch (e) {
      toast(errorMessage(e), "error");
      return;
    }
    qc.clear();
    authStore.set("out");
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line px-4 pb-2">
        <h1 className="pt-2 text-xl font-bold" tabIndex={-1} data-route-heading>
          Settings
        </h1>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <Section title="Appearance">
          <ThemePicker />
          <Segmented<FontId>
            legend="Reading font"
            value={p.font}
            onChange={(font) => updatePrefs({ font })}
            options={[
              { value: "default", label: "Default" },
              { value: "easy", label: "Easy to read" },
            ]}
          />
          <Segmented<number>
            legend="Text size"
            value={p.textSize}
            onChange={(textSize) => updatePrefs({ textSize })}
            options={TEXT_SIZES.map((v, i) => ({ value: v, label: TEXT_SIZE_LABELS[i] ?? String(v) }))}
          />
          {p.adjustSeparately ? (
            <>
              <Segmented<Step> legend="Lists" hint="How much fits on screen." value={p.listDensity} onChange={(listDensity) => updatePrefs({ listDensity })} options={stepOptions} />
              <Segmented<Step> legend="Reading" hint="How much space between lines and paragraphs." value={p.readingDensity} onChange={(readingDensity) => updatePrefs({ readingDensity })} options={stepOptions} />
            </>
          ) : (
            <Segmented<Step>
              legend="Density"
              hint="How much fits on screen, and how much space there is in articles."
              value={p.listDensity}
              onChange={(v) => updatePrefs({ listDensity: v, readingDensity: v })}
              options={stepOptions}
            />
          )}
          <DensityPreview list={p.listDensity} reading={p.readingDensity} />
          <label className="flex min-h-11 items-center gap-3 text-sm">
            <input
              type="checkbox"
              checked={p.adjustSeparately}
              onChange={(e) => updatePrefs({ adjustSeparately: e.target.checked })}
              className="size-5 accent-[var(--kp-accent)]"
            />
            Adjust lists and reading separately
          </label>
        </Section>

        <Section title="Keyboard">
          <label htmlFor={switchId} className="flex min-h-11 items-start gap-3 text-sm">
            <input
              id={switchId}
              type="checkbox"
              checked={p.shortcuts}
              onChange={(e) => updatePrefs({ shortcuts: e.target.checked })}
              className="mt-0.5 size-5 accent-[var(--kp-accent)]"
            />
            <span>
              Single-key shortcuts
              <span className="block text-xs text-fg2">j and k move, s stars, m marks read, o opens the original. Turn off if letter keys clash with assistive technology.</span>
            </span>
          </label>
        </Section>

        <Section title="Account">
          <p className="text-sm text-fg2">
            {boot.data ? `Signed in as ${boot.data.user.username}.` : "Signed in."}
            {boot.data ? ` Kipple ${boot.data.version}.` : ""}
          </p>
          <Button onClick={() => void signOut()} className="self-start">
            Sign out
          </Button>
        </Section>
      </div>
    </div>
  );
}
