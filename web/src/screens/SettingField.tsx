import { useId, useRef, useState } from "react";
import { errorMessage } from "@/api/client";
import { settingsIssues, usePatchSettings, type SettingMeta } from "@/api/admin";
import { Segmented } from "@/ui/segmented";
import { Button } from "@/ui/button";
import { Field, Stepper, Switch, inputCls } from "@/ui/kit";
import { announce } from "@/shell/toasts";

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

/**
 * One setting, rendered from the server's metadata: a switch for bool, segmented buttons (up to 4 options)
 * or a select for enum, a stepper for int, a text box for text. `json` is never shown. Changes are sent as a
 * PATCH with an optimistic update; a 400 puts the server's message next to the control, and
 * "Reset to default" sends null.
 */
export function SettingField({ meta }: { meta: SettingMeta }) {
  const patch = usePatchSettings();
  const [error, setError] = useState<string | null>(null);
  // What the user has typed and not yet saved; null shows the saved value.
  const [typed, setDraft] = useState<string | null>(null);
  const draft = typed ?? String(meta.value ?? "");
  const uid = useId();

  if (meta.kind === "json") return null;

  // One PATCH per key at a time, latest value wins: while one is in flight, later values replace the
  // queued one, so responses can never arrive out of order and an older value never lands last.
  const inflight = useRef(false);
  const queued = useRef<{ value: unknown } | null>(null);
  const send = (value: unknown): void => {
    if (inflight.current) {
      queued.current = { value };
      return;
    }
    inflight.current = true;
    setError(null);
    patch.mutateAsync({ [meta.key]: value }).then(
      () => {
        inflight.current = false;
        const next = queued.current;
        queued.current = null;
        if (next) return send(next.value);
        setDraft(null);
        announce(`${meta.label} saved`);
      },
      (e: unknown) => {
        inflight.current = false;
        const next = queued.current;
        queued.current = null;
        if (next) return send(next.value);
        const bad = settingsIssues(e)?.issues.find((i) => i.key === meta.key);
        setError(bad ? bad.message : errorMessage(e));
      },
    );
  };

  const isDefault = same(meta.value, meta.default);
  const help = meta.description;
  const describedBy = [`${uid}-h`, error ? `${uid}-e` : ""].filter(Boolean).join(" ");

  let control;
  switch (meta.kind) {
    case "bool":
      control = <Switch label={meta.label} help={help} checked={meta.value === true} onChange={send} error={error} />;
      break;
    case "enum": {
      const opts = meta.options ?? [];
      const err = error ? (
        <p role="alert" className="mt-1 text-sm text-danger">
          {error}
        </p>
      ) : null;
      control =
        opts.length <= 4 ? (
          <>
            <Segmented<string | number> legend={meta.label} hint={help} value={meta.value as string | number} options={opts} onChange={send} />
            {err}
          </>
        ) : (
          <Field label={meta.label} help={help} error={error}>
            {(a) => (
              <select {...a} value={String(meta.value)} onChange={(e) => send(opts.find((o) => String(o.value) === e.target.value)?.value ?? e.target.value)} className={inputCls}>
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
    case "int":
      control = (
        <div>
          <div className="mb-1 text-sm font-semibold" id={`${uid}-l`}>
            {meta.label}
          </div>
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
              send(n);
            }}
          />
          <p id={`${uid}-h`} className="mt-1 text-xs text-fg2">
            {help}
          </p>
          {error ? (
            <p id={`${uid}-e`} role="alert" className="mt-1 text-sm text-danger">
              {error}
            </p>
          ) : null}
        </div>
      );
      break;
    default:
      control = (
        <Field label={meta.label} help={help} error={error}>
          {(a) => (
            <input
              {...a}
              type="text"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onBlur={() => draft !== String(meta.value ?? "") && send(draft)}
              onKeyDown={(e) => e.key === "Enter" && draft !== String(meta.value ?? "") && send(draft)}
              className={inputCls}
            />
          )}
        </Field>
      );
  }

  return (
    <div data-setting={meta.key} className="flex flex-col gap-1">
      {control}
      {!isDefault ? (
        <Button variant="link" className="min-h-11 self-start px-0" aria-label={`Reset ${meta.label} to default`} onClick={() => send(null)}>
          Reset to default
        </Button>
      ) : null}
    </div>
  );
}
