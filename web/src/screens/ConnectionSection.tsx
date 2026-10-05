import { useId, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { settingsIssues, useSettings, usePatchSettings, type SettingMeta } from "@/api/admin";
import { ApiError, errorMessage } from "@/api/client";
import { useBootstrap } from "@/api/queries";
import { accountError } from "./AccountSection";
import { keys } from "@/api/queries";
import { announce } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Field, Notice, Skeleton, inputCls } from "@/ui/kit";

/** The reachability settings: how other devices reach Kipple and who may. */
export const CONNECTION_KEYS = {
  publicUrl: "server.public_url",
  allowedHosts: "security.allowed_hosts",
  trustedProxies: "security.trusted_proxies",
  access: "security.cloudflare_access",
} as const;

export interface AccessValue {
  team_domain?: string;
  aud?: string;
}

/** A typed list (one entry per line, or separated by commas or spaces) as the list the server takes. */
export function parseList(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

/** What the server's access_in_use refusal means for the person. */
const ACCESS_IN_USE =
  "This account has no web password and signs in through Cloudflare Access. Set a web password first (under Account, above), then change or turn off Access.";

/** What the server's proxy_is_you refusal means for the person. */
const PROXY_IS_YOU =
  "Your own address is in this list. Kipple runs without a password, and then it refuses every request from a trusted proxy, so saving this would lock you out. Leave your address out, or set a password first.";

/**
 * Whether a write to the trusted proxies or Cloudflare Access must carry the web password: an account with one. Without
 * one, the server takes the Cloudflare Access sign-in (or, in open mode, the open gate) as the proof.
 */
function useNeedsPassword(): boolean {
  const user = useBootstrap().data?.user;
  return user?.password_set === true && user.auth_mode !== "open";
}

/** The web password a guarded write asks for. */
function CurrentPassword({ what, value, onChange }: { what: string; value: string; onChange: (v: string) => void }) {
  return (
    <Field label={`Your web password, to save ${what}`} help="Kipple asks for it before it changes who may sign in or name a visitor's address.">
      {(a) => <input {...a} type="password" autoComplete="current-password" value={value} onChange={(e) => onChange(e.target.value)} className={inputCls} />}
    </Field>
  );
}

const listText = (v: unknown): string => (Array.isArray(v) ? v.map(String).join("\n") : "");

/**
 * Saves one setting and reports the server's answer: the message of its issue for a refused value, or the error's
 * own message (such as access_in_use) for anything else.
 */
function useSaveSetting(meta: SettingMeta) {
  const patch = usePatchSettings();
  const qc = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const save = async (value: unknown, current?: string): Promise<boolean> => {
    setError(null);
    try {
      await patch.mutateAsync(current === undefined ? { [meta.key]: value } : { [meta.key]: value, current });
      announce(`${meta.label} saved`);
      // Whether Access validation is on is part of the account summary (Settings, Account).
      if (meta.key === CONNECTION_KEYS.access) void qc.invalidateQueries({ queryKey: keys.me });
      return true;
    } catch (e) {
      const bad = settingsIssues(e)?.issues.find((i) => i.key === meta.key);
      if (bad) setError(bad.message);
      else if (e instanceof ApiError && e.code === "access_in_use") setError(ACCESS_IN_USE);
      else if (e instanceof ApiError && e.code === "proxy_is_you") setError(PROXY_IS_YOU);
      else setError(accountError(e));
      return false;
    }
  };
  return { save, error, busy: patch.isPending };
}

/** The public URL: one address, saved with its own button. */
function PublicUrlField({ meta }: { meta: SettingMeta }) {
  const saved = typeof meta.value === "string" ? meta.value : "";
  const [typed, setTyped] = useState<string | null>(null);
  const { save, error, busy } = useSaveSetting(meta);
  const draft = typed ?? saved;
  const changed = draft.trim() !== saved;
  const submit = async () => {
    if (await save(draft.trim())) setTyped(null);
  };
  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        if (changed) void submit();
      }}
    >
      <Field label={meta.label} help={meta.description} error={error}>
        {(a) => (
          <input
            {...a}
            type="text"
            inputMode="url"
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
            placeholder="https://rss.example.com"
            value={draft}
            onChange={(e) => setTyped(e.target.value)}
            className={inputCls}
          />
        )}
      </Field>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" aria-label={`Save ${meta.label}`} disabled={busy || !changed}>
          {busy ? "Saving" : "Save"}
        </Button>
        {saved !== "" ? (
          <Button
            type="button"
            variant="ghost"
            aria-label={`Remove the ${meta.label.toLowerCase()}`}
            disabled={busy}
            onClick={() => {
              void save("").then((ok) => ok && setTyped(null));
            }}
          >
            Remove
          </Button>
        ) : null}
      </div>
    </form>
  );
}

/**
 * A list setting (allowed host names, trusted proxies): one entry per line. `guarded`: the write carries the web
 * password (the trusted proxies), asked for when the account has one.
 */
function ListField({ meta, placeholder, guarded = false }: { meta: SettingMeta; placeholder: string; guarded?: boolean }) {
  const saved = listText(meta.value);
  const [typed, setTyped] = useState<string | null>(null);
  const [current, setCurrent] = useState("");
  const { save, error, busy } = useSaveSetting(meta);
  const askPassword = useNeedsPassword() && guarded;
  const draft = typed ?? saved;
  const changed = parseList(draft).join("\n") !== parseList(saved).join("\n");
  return (
    <div className="flex flex-col gap-2">
      <Field label={meta.label} help={<>{meta.description} One per line.</>} error={error}>
        {(a) => (
          <textarea
            {...a}
            rows={3}
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
            placeholder={placeholder}
            value={draft}
            onChange={(e) => setTyped(e.target.value)}
            className={`${inputCls} font-mono text-sm`}
          />
        )}
      </Field>
      {askPassword && changed ? <CurrentPassword what={meta.label.toLowerCase()} value={current} onChange={setCurrent} /> : null}
      <div>
        <Button
          aria-label={`Save ${meta.label}`}
          disabled={busy || !changed || (askPassword && current === "")}
          onClick={() => {
            void save(parseList(draft), askPassword ? current : undefined).then((ok) => {
              if (ok) {
                setTyped(null);
                setCurrent("");
              }
            });
          }}
        >
          {busy ? "Saving" : "Save"}
        </Button>
      </div>
    </div>
  );
}

/** Cloudflare Access: the team domain and the audience tag, saved together (both or neither). */
function AccessField({ meta }: { meta: SettingMeta }) {
  const v = (meta.value ?? {}) as AccessValue;
  const saved = { team: v.team_domain ?? "", aud: v.aud ?? "" };
  const on = saved.team !== "" && saved.aud !== "";
  const [team, setTeam] = useState<string | null>(null);
  const [aud, setAud] = useState<string | null>(null);
  const [current, setCurrent] = useState("");
  const { save, error, busy } = useSaveSetting(meta);
  const askPassword = useNeedsPassword();
  const errorId = useId();
  const teamDraft = team ?? saved.team;
  const audDraft = aud ?? saved.aud;
  const changed = teamDraft.trim() !== saved.team || audDraft.trim() !== saved.aud;
  const headingId = useId();
  const reset = (ok: boolean) => {
    if (ok) {
      setTeam(null);
      setAud(null);
      setCurrent("");
    }
  };
  const sendCurrent = askPassword ? current : undefined;
  /** The inputs point at the error notice too, so a screen reader says why the save failed. */
  const describe = (a: { "aria-describedby"?: string }) => ({
    "aria-describedby": [a["aria-describedby"], error ? errorId : ""].filter(Boolean).join(" ") || undefined,
    "aria-invalid": error ? true : undefined,
  });
  return (
    <fieldset className="flex flex-col gap-3" aria-labelledby={headingId}>
      <legend id={headingId} className="text-sm font-semibold">
        {meta.label}
      </legend>
      <p className="text-xs text-fg2">{meta.description}</p>
      <p className="text-sm" role="status">
        {on ? (
          <>
            On: Kipple checks the Access token of <span className="font-mono break-all">{saved.team}</span>.
          </>
        ) : (
          "Off: Kipple does not check Access tokens."
        )}
      </p>
      {error ? (
        <div id={errorId}>
          <Notice tone="error">{error}</Notice>
        </div>
      ) : null}
      <Field label="Team domain">
        {(a) => (
          <input
            {...a}
            {...describe(a)}
            type="text"
            inputMode="url"
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
            placeholder="yourteam.cloudflareaccess.com"
            value={teamDraft}
            onChange={(e) => setTeam(e.target.value)}
            className={inputCls}
          />
        )}
      </Field>
      <Field label="Application audience (AUD) tag">
        {(a) => (
          <input
            {...a}
            {...describe(a)}
            type="text"
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
            value={audDraft}
            onChange={(e) => setAud(e.target.value)}
            className={`${inputCls} font-mono text-sm`}
          />
        )}
      </Field>
      {askPassword && (changed || on) ? <CurrentPassword what={meta.label} value={current} onChange={setCurrent} /> : null}
      <div className="flex flex-wrap gap-2">
        <Button
          aria-label={`Save ${meta.label}`}
          disabled={busy || !changed || (askPassword && current === "")}
          onClick={() => void save({ team_domain: teamDraft.trim(), aud: audDraft.trim() }, sendCurrent).then(reset)}
        >
          {busy ? "Saving" : "Save"}
        </Button>
        {on ? (
          <Button variant="ghost" aria-label={`Turn off ${meta.label}`} disabled={busy || (askPassword && current === "")} onClick={() => void save({}, sendCurrent).then(reset)}>
            Turn off
          </Button>
        ) : null}
      </div>
    </fieldset>
  );
}

/**
 * Settings, Account & Devices, "Address and access": the public URL, the allowed host names, the trusted proxies and
 * Cloudflare Access. Each saves on its own, and the server applies it at once.
 */
export function ConnectionSection() {
  const settings = useSettings();
  if (settings.isPending) return <Skeleton rows={3} label="Loading settings" />;
  if (!settings.data) {
    return (
      <Notice tone="error">
        Couldn't load your settings. {errorMessage(settings.error)}{" "}
        <Button variant="link" onClick={() => void settings.refetch()}>
          Try again
        </Button>
      </Notice>
    );
  }
  const all = settings.data.settings;
  const meta = (key: string) => all.find((s) => s.key === key);
  const url = meta(CONNECTION_KEYS.publicUrl);
  const hosts = meta(CONNECTION_KEYS.allowedHosts);
  const proxies = meta(CONNECTION_KEYS.trustedProxies);
  const access = meta(CONNECTION_KEYS.access);
  return (
    <div className="flex flex-col gap-6">
      <p className="text-sm text-fg2">How your other devices reach Kipple, and who may. Changes apply at once; Kipple does not need a restart.</p>
      {url ? <PublicUrlField meta={url} /> : null}
      {hosts ? <ListField meta={hosts} placeholder="For example nas.local" /> : null}
      {proxies ? <ListField meta={proxies} placeholder="For example 192.0.2.10" guarded /> : null}
      {access ? <AccessField meta={access} /> : null}
    </div>
  );
}
