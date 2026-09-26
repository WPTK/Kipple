import { useId, useRef, useState } from "react";
import { errorMessage } from "@/api/client";
import { settingsIssues, usePatchSettings, type SettingMeta } from "@/api/admin";
import { settingWarning } from "@/lib/settingGuards";
import { Segmented } from "@/ui/segmented";
import { Button } from "@/ui/button";
import { Field, Modal, Stepper, Switch, inputCls } from "@/ui/kit";
import { announce } from "@/shell/toasts";

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

/**
 * One setting, rendered from the server's metadata: a switch for bool, segmented buttons (up to 4 options)
 * or a select for enum, a stepper for int, a text box for text. `json` is never shown. Changes are sent as a
 * PATCH with an optimistic update; a 400 puts the server's message next to the control, and
 * "Reset to default" sends null.
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
  const queued = useRef<{ value: unknown } | null>(null);

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

  // A change the server acts on at once (a lower cache cap, fewer articles kept) waits for a yes.
  const ask = (value: unknown): void => {
    const w = settingWarning(meta.key, meta.value, value === null ? meta.default : value);
    if (w) setConfirm({ value, w });
    else send(value);
  };

  const cancelAsk = (): void => {
    setConfirm(null);
    setDraft(null); // a stepper that was typed into shows the saved number again
  };

  const isDefault = same(meta.value, meta.default);
  const help = meta.description;
  const describedBy = [`${uid}-h`, error ? `${uid}-e` : ""].filter(Boolean).join(" ");

  let control;
  switch (meta.kind) {
    case "bool":
      control = <Switch label={meta.label} help={help} checked={meta.value === true} onChange={ask} error={error} />;
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
          {error ? (
            <p id={`${uid}-e`} role="alert" className="mt-1 text-sm text-danger">
              {error}
            </p>
          ) : null}
        </div>
      );
      break;
    }
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
              <Button variant="ghost" onClick={cancelAsk}>
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
      {!isDefault ? (
        <Button variant="link" className="min-h-11 self-start px-0" aria-label={`Reset ${meta.label} to default`} onClick={() => ask(null)}>
          Reset to default
        </Button>
      ) : null}
    </div>
  );
}
