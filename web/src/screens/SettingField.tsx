import { useId, useRef, useState } from "react";
import { errorMessage } from "@/api/client";
import { settingsIssues, usePatchSettings, type SettingMeta } from "@/api/admin";
import { lowersRetention, settingWarning } from "@/lib/settingGuards";
import { RetentionDialog } from "./RetentionDialog";
import { Segmented } from "@/ui/segmented";
import { Button } from "@/ui/button";
import { Field, FieldError, Modal, Stepper, Switch, inputCls } from "@/ui/kit";
import { HelpText, SavedMark, useSavedMark } from "./settingUi";

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

/**
 * One setting, rendered from the server's metadata: a switch for bool, segmented buttons (up to 4 options)
 * or a select for enum, a stepper for int, a text box for text. `json` is never shown. Changes are sent as a
 * PATCH with an optimistic update; a 400 puts the server's message next to the control, and
 * "Reset to default" sends null. One save model for all of them: a switch, a choice or a number saves the moment it
 * changes, a text box saves with its Save button (Revert puts the saved text back), and each shows the same visible
 * "Saved" when the server has it. A number that is not valid goes back to the saved value, with the reason.
 */
export function SettingField({ meta, presets }: { meta: SettingMeta; presets?: readonly { value: number; label: string }[] }) {
  const patch = usePatchSettings();
  // With presets, the stepper (Custom) shows only when asked for, or when the value is not one of the presets.
  const [customOpen, setCustomOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // What the user has typed and not yet saved; null shows the saved value.
  const [typed, setDraft] = useState<string | null>(null);
  const draft = typed ?? String(meta.value ?? "");
  const uid = useId();
  const inflight = useRef(false);
  const [confirm, setConfirm] = useState<{ value: unknown; w: { title: string; body: string; action: string } } | null>(null);
  // A lower number of articles to keep: the dialog that says how many articles that removes.
  const [trim, setTrim] = useState<{ value: unknown } | null>(null);
  const queued = useRef<{ value: unknown } | null>(null);
  const mark = useSavedMark();

  if (meta.kind === "json") return null;

  // One PATCH per key at a time, latest value wins: while one is in flight, later values replace the
  // queued one, so responses can never arrive out of order and an older value never lands last.
  const send = (value: unknown): void => {
    if (inflight.current) {
      queued.current = { value };
      return;
    }
    inflight.current = true;
    setError(null);
    mark.hide();
    patch.mutateAsync({ [meta.key]: value }).then(
      () => {
        inflight.current = false;
        const next = queued.current;
        queued.current = null;
        if (next) return send(next.value);
        setDraft(null);
        mark.flash();
      },
      (e: unknown) => {
        inflight.current = false;
        const next = queued.current;
        queued.current = null;
        if (next) return send(next.value);
        const bad = settingsIssues(e)?.issues.find((i) => i.key === meta.key);
        setError(bad ? bad.message : errorMessage(e));
        // A refused number or choice shows the saved value again; only a text box keeps what was typed, to be fixed.
        if (meta.kind !== "text") setDraft(null);
      },
    );
  };

  // A change the server acts on at once (a lower cache cap, fewer articles kept) waits for a yes.
  const ask = (value: unknown): void => {
    if (meta.key === "retention.default" && lowersRetention(meta.value, value === null ? meta.default : value)) return setTrim({ value });
    const w = settingWarning(meta.key, meta.value, value === null ? meta.default : value);
    if (w) setConfirm({ value, w });
    else send(value);
  };

  const cancelAsk = (): void => {
    setConfirm(null);
    setDraft(null); // a stepper that was typed into shows the saved number again
  };

  const isDefault = same(meta.value, meta.default);
  const help = <HelpText text={meta.description} label={meta.label} />;
  const describedBy = [`${uid}-h`, error ? `${uid}-e` : ""].filter(Boolean).join(" ");

  let control;
  switch (meta.kind) {
    case "bool":
      control = <Switch label={meta.label} help={help} checked={meta.value === true} onChange={ask} error={error} />;
      break;
    case "enum": {
      const opts = meta.options ?? [];
      const err = error ? <FieldError id={`${uid}-e`} className="mt-1">{error}</FieldError> : null;
      control =
        opts.length <= 4 ? (
          <>
            <Segmented<string | number> legend={meta.label} hint={help} value={meta.value as string | number} options={opts} onChange={ask} explicit />
            {err}
          </>
        ) : (
          <Field label={meta.label} help={help} error={error}>
            {(a) => (
              <select {...a} value={String(meta.value)} onChange={(e) => ask(opts.find((o) => String(o.value) === e.target.value)?.value ?? e.target.value)} className={inputCls}>
                {opts.map((o) => (
                  <option key={String(o.value)} value={String(o.value)}>
                    {o.label}
                  </option>
                ))}
              </select>
            )}
          </Field>
        );
      break;
    }
    case "int": {
      const current = Number(draft) || 0;
      const onPreset = !!presets && presets.some((p) => p.value === current);
      const stepper = !presets || customOpen || !onPreset;
      control = (
        <div>
          {presets ? (
            <Segmented<string | number>
              legend={meta.label}
              value={stepper ? "custom" : current}
              wrap
              explicit
              options={[...presets.map((p) => ({ value: p.value as string | number, label: p.label })), { value: "custom", label: "Custom" }]}
              onChange={(v) => {
                if (v === "custom") return setCustomOpen(true);
                setCustomOpen(false);
                if (settingWarning(meta.key, meta.value, v)) return ask(v);
                setDraft(String(v));
                send(v);
              }}
            />
          ) : (
            <div className="mb-1 text-sm font-semibold" id={`${uid}-l`}>
              {meta.label}
            </div>
          )}
          {stepper ? <div className={presets ? "mt-2" : undefined}>
          <Stepper
            label={meta.label}
            value={Number(draft) || 0}
            min={meta.min}
            max={meta.max}
            step={meta.step}
            unit={meta.unit}
            describedBy={describedBy}
            invalid={!!error}
            onChange={(n) => {
              setDraft(String(n));
              ask(n);
            }}
          />
          </div> : null}
          <p id={`${uid}-h`} className="mt-1 text-xs text-fg2">
            {help}
          </p>
          {error ? <FieldError id={`${uid}-e`} className="mt-1">{error}</FieldError> : null}
        </div>
      );
      break;
    }
    default: {
      const changed = draft !== String(meta.value ?? "");
      control = (
        <form
          className="flex flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (changed) send(draft);
          }}
        >
          <Field label={meta.label} help={help} error={error}>
            {(a) => (
              <input
                {...a}
                type="text"
                value={draft}
                onChange={(e) => {
                  setDraft(e.target.value);
                  setError(null);
                  mark.hide();
                }}
                className={inputCls}
              />
            )}
          </Field>
          <div className="flex flex-wrap items-center gap-2">
            <Button type="submit" aria-label={`Save ${meta.label}`} disabled={!changed || patch.isPending}>
              {patch.isPending ? "Saving" : "Save"}
            </Button>
            {changed ? (
              <Button
                variant="ghost"
                aria-label={`Revert ${meta.label}`}
                onClick={() => {
                  setDraft(null);
                  setError(null);
                }}
              >
                Revert
              </Button>
            ) : null}
          </div>
        </form>
      );
    }
  }

  return (
    <div data-setting={meta.key} className="flex flex-col gap-1">
      {control}
      {trim ? (
        <RetentionDialog
          keep={Number(trim.value === null ? meta.default : trim.value)}
          apply={false}
          onConfirm={() => send(trim.value)}
          onClose={() => setTrim(null)}
        />
      ) : null}
      {confirm ? (
        <Modal
          open
          onOpenChange={(o) => {
            if (!o) cancelAsk();
          }}
          title={confirm.w.title}
          description={confirm.w.body}
          footer={
            <>
              <Button onClick={cancelAsk}>
                Cancel
              </Button>
              <Button
                variant="solid"
                onClick={() => {
                  const v = confirm.value;
                  setConfirm(null);
                  if (typeof v === "number") setDraft(String(v));
                  send(v);
                }}
              >
                {confirm.w.action}
              </Button>
            </>
          }
        />
      ) : null}
      <div className="flex flex-wrap items-center gap-x-4">
        {!isDefault ? (
          <Button variant="link" className="min-h-11 px-0" aria-label={`Reset ${meta.label} to default`} onClick={() => ask(null)}>
            Reset to default
          </Button>
        ) : null}
        <SavedMark show={mark.saved} />
      </div>
    </div>
  );
}
