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
      if (!/^#\d+$/.test(String(issue))) out.push(`knownFail ${flow} (${engine}): needs an issue reference like "#123", got ${JSON.stringify(issue)}`);
    }
  }
  return out;
}

/**
 * The verdict of one flow run on one engine. `issue` is its knownFail entry, if any. Returns the label to print, whether it
 * counts as `ok`, `known` or `fail`, and the exit code it contributes: a listed flow that fails is `known` (exit 0), one that
 * passes is a `fail` so the stale entry gets removed.
 */
export function legVerdict(flow, code, issue, suffix = "") {
  const tail = `(exit ${code}${suffix})`;
  if (!issue) return { status: code === 0 ? "ok" : "fail", exit: code, label: `${code === 0 ? "ok  " : "FAIL"} ${flow} ${tail}` };
  if (code === 0) return { status: "fail", exit: 1, label: `FAIL ${flow} passed but is listed in knownFail (${issue}): remove the entry` };
  return { status: "known", exit: 0, label: `known (issue ${issue}) ${flow} ${tail}` };
}
