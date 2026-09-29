import { useRef, useState } from "react";
import { usePatchSettings } from "@/api/admin";
import { errorMessage } from "@/api/client";
import { useStore } from "@/lib/store";
import { DEFAULT_DAY, DEFAULT_NIGHT, schemeById, type Scheme } from "@/theme/schemes";
import { useAllowedSchemes } from "@/theme/serverThemes";
import { SchemeSelect } from "@/theme/ThemePicker";
import { choosePair } from "@/theme/settings";
import { themeStore, updateTheme } from "@/theme/theme";
import { Button } from "@/ui/button";
import { Notice } from "@/ui/kit";
import { StepActions, WizardFrame } from "./Frame";
import { stepById } from "./steps";

/** A small sample article in a scheme's own colors, so both picks can be seen at once whatever the page is showing. */
function Sample({ heading, scheme }: { heading: string; scheme: Scheme }) {
  const t = scheme.tokens;
  return (
    <div className="flex flex-col gap-1">
      <p className="text-xs font-semibold text-fg2">{heading}</p>
      <div data-testid={`sample-${heading.toLowerCase()}`} data-scheme={scheme.id} className="flex flex-col gap-2 rounded-xl border p-3" style={{ background: t.bg, color: t.text, borderColor: t.border }}>
        <p className="text-xs" style={{ color: t.text2 }}>
          The Ubiquitous Gazette · 2h
        </p>
        <p className="text-base leading-snug font-bold">Toaster files formal grievance against the kitchen</p>
        <p className="text-sm leading-snug" style={{ color: t.text2 }}>
          Officials confirm the appliance has hired counsel.
        </p>
        <div className="flex items-center gap-2 text-xs">
          <span className="rounded-full px-2 py-0.5 font-semibold" style={{ background: t.accent, color: t.bg }}>
            Unread
          </span>
          <span style={{ color: t.link }}>Read the full story</span>
        </div>
        <p className="text-xs font-semibold" style={{ color: t.text2 }}>
          {scheme.name}
        </p>
      </div>
    </div>
  );
}

/**
 * Step 4: the look. A day theme and a night theme, which Kipple switches between with each device's own light or dark
 * setting (the same pair Settings calls "Follow system"). Picking applies at once on this device, so the page itself is
 * the preview; Continue also saves the pair as the default for every device that has not chosen its own. Skip puts
 * this device back the way it was.
 */
export function ThemeStep({ onBack, onNext, onSkipAll, skipAllBusy }: { onBack: () => void; onNext: () => void; onSkipAll: () => void; skipAllBusy?: boolean }) {
  const t = useStore(themeStore);
  const schemes = useAllowedSchemes();
  const patch = usePatchSettings();
  const before = useRef(t);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const offered = (id: string) => schemes.some((s) => s.id === id);
  const first = schemes[0]?.id ?? DEFAULT_DAY;
  const day = offered(t.day) ? t.day : offered(DEFAULT_DAY) ? DEFAULT_DAY : first;
  const night = offered(t.night) ? t.night : offered(DEFAULT_NIGHT) ? DEFAULT_NIGHT : first;

  const pick = (which: "day" | "night", id: string) => {
    // Follow system without a schedule: the two picks, whichever the device is showing now. Any earlier schedule or fixed pick gives way.
    updateTheme({ ...choosePair("follow"), day, night, [which]: id });
    setError(null);
  };

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      await patch.mutateAsync({ "ui.theme": "system", "ui.theme_day": day, "ui.theme_night": night });
      onNext();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const skip = () => {
    updateTheme(before.current);
    onNext();
  };

  return (
    <WizardFrame
      step={stepById("theme")}
      description="Choose one look for the daytime and one for the evening. Kipple switches between them with your device's light or dark setting."
      onSkipAll={() => {
        updateTheme(before.current);
        onSkipAll();
      }}
      skipAllBusy={skipAllBusy}
    >
      <div className="flex flex-1 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        <div className="grid grid-cols-1 gap-3 min-[420px]:grid-cols-2">
          <SchemeSelect label="Day theme" value={day} onChange={(id) => pick("day", id)} schemes={schemes} />
          <SchemeSelect label="Night theme" value={night} onChange={(id) => pick("night", id)} schemes={schemes} />
        </div>
        <div className="grid grid-cols-1 gap-3 min-[420px]:grid-cols-2" aria-label="Preview of your two themes" role="group">
          <Sample heading="Day" scheme={schemeById(day)} />
          <Sample heading="Night" scheme={schemeById(night)} />
        </div>
        <p className="text-xs text-fg2">This page already shows your pick for this device. You can also choose a schedule, or a single theme, in Settings, Appearance &amp; Reading, any time.</p>
        <StepActions
          back={
            <Button onClick={onBack}>
              Back
            </Button>
          }
        >
          <Button onClick={skip}>Skip</Button>
          <Button variant="solid" disabled={busy} onClick={() => void save()}>
            {busy ? "Saving" : "Continue"}
          </Button>
        </StepActions>
      </div>
    </WizardFrame>
  );
}
