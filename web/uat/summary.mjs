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
