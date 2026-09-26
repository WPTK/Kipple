import { useId, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Smartphone } from "lucide-react";
import { ApiError, errorMessage } from "@/api/client";
import { describeUserAgent, deleteDevice, devicesKey, renameDevice, useDevices, type DeviceRow } from "@/api/devices";
import { copySettingsFrom, makeThisDeviceDefault, syncStore } from "@/lib/deviceSync";
import { useStore } from "@/lib/store";
import { whenLabel } from "@/lib/format";
import { announce, toast } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Modal, Notice, Skeleton, inputCls } from "@/ui/kit";

const label = (d: DeviceRow): string => d.name || describeUserAgent(d.user_agent, d.client);

type Confirm = { kind: "copy"; from: DeviceRow } | { kind: "delete"; device: DeviceRow } | { kind: "default" } | { kind: "reset" };

function ThisDeviceName({ device }: { device: DeviceRow }) {
  const qc = useQueryClient();
  const id = useId();
  // What has been typed and not yet saved; null shows the saved name.
  const [typed, setTyped] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const value = typed ?? device.name;
  const dirty = typed !== null && typed.trim() !== device.name;
  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      await renameDevice(typed?.trim() ?? "");
      setTyped(null);
      await qc.invalidateQueries({ queryKey: devicesKey });
      announce("Device name saved");
    } catch (e) {
      setError(e instanceof ApiError && typeof e.body?.message === "string" ? e.body.message : errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      className="flex flex-col gap-1"
      onSubmit={(e) => {
        e.preventDefault();
        if (dirty) void save();
      }}
    >
      <label htmlFor={id} className="text-sm font-semibold">
        This device's name
      </label>
      <div className="flex gap-2">
        <input
          id={id}
          value={value}
          maxLength={64}
          placeholder={describeUserAgent(device.user_agent, device.client)}
          aria-describedby={`${id}-h`}
          aria-invalid={error ? true : undefined}
          onChange={(e) => setTyped(e.target.value)}
          className={inputCls}
        />
        <Button type="submit" disabled={!dirty || busy}>
          Save name
        </Button>
      </div>
      <p id={`${id}-h`} className="text-xs text-fg2">
        Shown in this list so you can tell your devices apart. Leave it empty to use a description of the browser.
      </p>
      {error ? (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      ) : null}
    </form>
  );
}

function confirmCopy(c: Confirm): { title: string; body: string; action: string } {
  switch (c.kind) {
    case "copy":
      return {
        title: "Copy settings here?",
        body: `This replaces this device's own settings with the ones ${label(c.from)} has. Anything you changed on this device is lost.`,
        action: "Copy settings",
      };
    case "delete":
      return {
        title: "Forget this device?",
        body: `${label(c.device)} and its settings are removed. If it opens Kipple again it starts over as a new device.`,
        action: "Forget device",
      };
    case "default":
      return {
        title: "Use these settings as the default?",
        body: "Devices that have no settings of their own yet will start from this device's theme, font, layout and other choices. Devices that already have their own settings keep them.",
        action: "Use as default",
      };
    case "reset":
      return {
        title: "Reset this device to defaults?",
        body: "This clears every choice made on this device (theme, font, layout, keyboard and the rest) so it uses the defaults again. Your feeds, articles and other devices are not touched.",
        action: "Reset this device",
      };
  }
}

/** Settings > Devices: this device's name, every device with when it was last seen, and copy, default, reset and forget. */
export function DevicesSection() {
  const qc = useQueryClient();
  const devices = useDevices();
  const [confirm, setConfirm] = useState<Confirm | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The server could not register this browser (docs/design.md 7.1c): its settings stay local and nothing is sent.
  const unsaved = useStore(syncStore).status === "unsaved";
  const list = devices.data ?? [];
  const me = unsaved ? undefined : list.find((d) => d.current);
  const others = list.filter((d) => !d.current);

  const run = async () => {
    if (!confirm) return;
    setBusy(true);
    setError(null);
    try {
      if (confirm.kind === "copy") {
        await copySettingsFrom(confirm.from.id);
        toast("Settings copied to this device");
      } else if (confirm.kind === "reset") {
        await copySettingsFrom("defaults");
        toast("This device is back to the defaults");
      } else if (confirm.kind === "default") {
        await makeThisDeviceDefault();
        toast("New devices will start from this device's settings");
      } else {
        await deleteDevice(confirm.device.id);
        announce("Device forgotten");
      }
      await qc.invalidateQueries({ queryKey: devicesKey });
      setConfirm(null);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const c = confirm ? confirmCopy(confirm) : null;
  return (
    <>
      <p className="text-sm text-fg2">
        Each browser or installed app is a device with its own theme, font, layout and other choices. They are saved on the server, so they survive clearing the browser.
      </p>
      {unsaved ? (
        <Notice tone="warn" role="status">
          This browser can't save its own settings yet. Older browsers will be forgotten automatically. Until then it keeps using the settings stored in this browser.
        </Notice>
      ) : null}
      {devices.isPending ? <Skeleton rows={2} label="Loading devices" /> : null}
      {devices.isError ? (
        <Notice tone="error">
          Couldn't load your devices. {errorMessage(devices.error)}{" "}
          <Button variant="link" onClick={() => void devices.refetch()}>
            Try again
          </Button>
        </Notice>
      ) : null}
      {me ? <ThisDeviceName key={me.id} device={me} /> : null}
      {list.length ? (
        <ul aria-label="Your devices" className="flex flex-col gap-2">
          {list.map((d) => (
            <li key={d.id} className="flex flex-col gap-2 rounded-xl border border-line bg-surface p-3">
              <div className="flex items-start gap-3">
                <Smartphone aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-fg2" />
                <div className="min-w-0 flex-1">
                  <p className="truncate font-semibold">
                    {label(d)}
                    {d.current ? <span className="ml-2 rounded-full border border-accent px-2 py-0.5 text-xs font-medium">This device</span> : null}
                  </p>
                  <p className="text-xs text-fg2">
                    {d.current ? "Using it now" : `Last seen ${whenLabel(d.last_seen_at).toLowerCase()}`} ·{" "}
                    {d.overrides === 0 ? "uses the defaults" : `${d.overrides} setting${d.overrides === 1 ? "" : "s"} of its own`}
                  </p>
                  {d.name ? <p className="truncate text-xs text-fg2">{describeUserAgent(d.user_agent, d.client)}</p> : null}
                </div>
              </div>
              {d.current ? null : (
                <div className="flex flex-wrap gap-2">
                  {unsaved ? null : (
                    <Button onClick={() => setConfirm({ kind: "copy", from: d })} aria-label={`Copy settings from ${label(d)}`}>
                      Copy its settings here
                    </Button>
                  )}
                  <Button variant="ghost" onClick={() => setConfirm({ kind: "delete", device: d })} aria-label={`Forget ${label(d)}`}>
                    Forget
                  </Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      ) : null}
      {others.length === 0 && me ? <p className="text-xs text-fg2">This is the only device so far. Open Kipple on another browser or your phone and it will show up here.</p> : null}
      {unsaved ? null : (
        <>
          <div className="flex flex-col items-start gap-1">
            <Button onClick={() => setConfirm({ kind: "default" })}>Use this device's settings as the default for new devices</Button>
            <p className="text-xs text-fg2">A device that has never changed a setting follows the default, so it stays in step when you update it.</p>
          </div>
          <div className="flex flex-col items-start gap-1">
            <Button variant="link" className="min-h-11 px-0" onClick={() => setConfirm({ kind: "reset" })}>
              Reset this device to defaults
            </Button>
          </div>
        </>
      )}
      {confirm && c ? (
        <Modal
          open
          onOpenChange={(o) => {
            if (!o && !busy) {
              setConfirm(null);
              setError(null);
            }
          }}
          title={c.title}
          description={c.body}
          footer={
            <>
              <Button variant="ghost" onClick={() => setConfirm(null)} disabled={busy}>
                Cancel
              </Button>
              <Button variant="solid" onClick={() => void run()} disabled={busy}>
                {busy ? "Working" : c.action}
              </Button>
            </>
          }
        >
          {error ? <Notice tone="error">{error}</Notice> : null}
        </Modal>
      ) : null}
    </>
  );
}
