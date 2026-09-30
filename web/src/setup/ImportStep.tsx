import { useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { importOpml, invalidateFeeds, type OpmlResult } from "@/api/admin";
import { OpmlResultSummary, opmlError, opmlFileProblem } from "@/screens/feeds/OpmlDialog";
import { announce } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Field, Notice, inputCls } from "@/ui/kit";
import { StepActions, WizardFrame } from "./Frame";
import { stepById } from "./steps";

/**
 * Step 5: feeds from another reader. The same file check, upload and report as Settings' Import OPML, in a page instead
 * of a dialog. Skippable: most people who don't have a file just move on.
 */
export function ImportStep({ onBack, onNext, onSkipAll, skipAllBusy }: { onBack: () => void; onNext: () => void; onSkipAll: () => void; skipAllBusy?: boolean }) {
  const qc = useQueryClient();
  const [file, setFile] = useState<File | null>(null);
  const [days, setDays] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<OpmlResult | null>(null);
  // The check of a file reads it, which takes a moment for a big one: only the latest pick may answer.
  const pick = useRef(0);

  const daysNum = days.trim() === "" ? undefined : Number(days);
  const daysBad = daysNum !== undefined && (!Number.isInteger(daysNum) || daysNum < 1 || daysNum > 365);

  const run = async () => {
    if (!file || daysBad) return;
    setBusy(true);
    setError(null);
    try {
      const r = await importOpml(file, daysNum);
      setResult(r);
      invalidateFeeds(qc);
      announce(`Imported ${r.feeds_added} feed${r.feeds_added === 1 ? "" : "s"}`);
    } catch (e) {
      setError(opmlError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <WizardFrame
      step={stepById("import")}
      description="Coming from another feed reader? Export your feeds from it as an OPML file (most readers have an Export option in their settings) and add the file here. If you don't have one, skip this."
      onSkipAll={onSkipAll}
      skipAllBusy={skipAllBusy}
    >
      <div className="flex flex-1 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {result ? (
          <div className="flex flex-col gap-3" data-testid="import-result">
            <h2 className="text-lg font-bold">Import finished</h2>
            <OpmlResultSummary result={result} />
          </div>
        ) : (
          <>
            <Field label="OPML file" help="Feeds you already have are left alone.">
              {(a) => (
                <input
                  {...a}
                  type="file"
                  accept=".opml,.xml,text/xml,application/xml,text/x-opml,*/*"
                  onChange={(e) => {
                    const f = e.target.files?.[0] ?? null;
                    const mine = ++pick.current;
                    setError(null);
                    setFile(null);
                    if (!f) return;
                    void opmlFileProblem(f).then((problem) => {
                      if (mine !== pick.current) return;
                      if (problem) setError(problem);
                      else setFile(f);
                    });
                  }}
                  className={`${inputCls} py-2`}
                />
              )}
            </Field>
            <Field
              label="Mark older articles as read (optional)"
              help="Enter a number of days. Articles older than that arrive already read, so a big import doesn't flood Unread."
              error={daysBad ? "Enter a whole number from 1 to 365, or leave empty." : null}
            >
              {(a) => <input {...a} inputMode="numeric" type="text" value={days} onChange={(e) => setDays(e.target.value)} placeholder="For example 7" className={inputCls} />}
            </Field>
          </>
        )}
        <StepActions back={<Button onClick={onBack}>Back</Button>}>
          {result ? (
            <Button variant="solid" onClick={onNext}>
              Continue
            </Button>
          ) : (
            <>
              <Button onClick={onNext}>Skip</Button>
              <Button variant="solid" disabled={!file || busy || daysBad} onClick={() => void run()}>
                {busy ? "Importing" : "Import"}
              </Button>
            </>
          )}
        </StepActions>
      </div>
    </WizardFrame>
  );
}
