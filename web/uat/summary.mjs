// What all.mjs prints under "Failures" for one flow, from its stderr, exit code and spawn error. Pure, so it is unit tested.

/**
 * The lines to show for a flow that did not exit 0: its `FAIL ` lines when it printed any, otherwise (a crash, a
 * thrown error, a flow that could not start) the spawn error or the last non-empty stderr line, so a failed flow is
 * never listed with nothing under it.
 */
export function failureLines(stderr, code, error) {
  const lines = String(stderr).split(/\r?\n/);
  const fails = lines.filter((l) => l.startsWith("FAIL "));
  if (fails.length || code === 0) return fails;
  if (error) return [`could not run: ${error.message}`];
  const last = lines.map((l) => l.trim()).filter(Boolean).pop();
  return [last ? `exit ${code} with no FAIL line; last stderr: ${last}` : `exit ${code} with no FAIL line and no stderr`];
}

const ENGINES = ["chromium", "webkit", "firefox"];

/**
 * Problems in the manifest's `knownFail` ({ "<flow>.mjs": { "<engine>": "#<issue>" } }): an unknown engine, a flow that
 * is not in that engine's list, or a value that is not an issue reference. An exemption without an issue is a problem,
 * so a known failure is always tracked somewhere.
 */
export function knownFailProblems(knownFail, manifest) {
  const out = [];
  for (const [flow, engines] of Object.entries(knownFail ?? {})) {
    for (const [engine, issue] of Object.entries(engines ?? {})) {
      if (!ENGINES.includes(engine)) out.push(`knownFail ${flow}: unknown engine ${engine} (chromium, webkit or firefox)`);
      else if (!manifest[engine].includes(flow)) out.push(`knownFail ${flow}: not a ${engine} flow`);
      if (!/^#[1-9]\d*$/.test(String(issue))) out.push(`knownFail ${flow} (${engine}): needs an issue reference like "#123", got ${JSON.stringify(issue)}`);
    }
  }
  return out;
}

/**
 * The verdict of one flow run on one engine. `issue` is its knownFail entry, if any; `error` the spawn error; `firstFail` the
 * flow's first FAIL line. Returns the label to print, whether it is `ok`, `known` or `fail`, and the exit code it contributes.
 * A listed flow is `known` only when it exits 1 (findings): a crash, a spawn error or exit 2 stays a failure, and a listed flow
 * that passes is a failure so the stale entry gets removed.
 */
export function legVerdict(flow, code, issue, error, firstFail = "") {
  const tail = `(exit ${code}${error ? `, ${error.message}` : ""})`;
  if (code === 0 && issue) return { status: "fail", exit: 1, label: `FAIL ${flow} passed but is listed in knownFail (${issue}): remove the entry` };
  if (code === 0) return { status: "ok", exit: 0, label: `ok   ${flow} ${tail}` };
  if (issue && code === 1 && !error) {
    return { status: "known", exit: 0, label: `known (issue ${issue}) ${flow} ${tail}`, detail: `known (issue ${issue}) ${flow}${firstFail ? `: ${firstFail}` : ""}` };
  }
  return { status: "fail", exit: code, label: `FAIL ${flow} ${tail}` };
}
