import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";

// A ratchet: the fixed sleeps in the UAT flows may only go down. Each one is a guess at how long the app takes; wait for
// the state instead (the waitFor* helpers in common.mjs, or locator and expect waits). A sleep that measures real time
// stays, with a one-line comment saying why.
const BASELINE = 74;

it("the number of fixed sleeps in web/uat does not grow", () => {
  const dir = dirname(fileURLToPath(import.meta.url));
  let count = 0;
  for (const f of readdirSync(dir).filter((n) => n.endsWith(".mjs") && !n.endsWith(".test.mjs"))) count += readFileSync(join(dir, f), "utf8").split("waitForTimeout(").length - 1;
  expect(
    count,
    `${count} waitForTimeout( calls in web/uat/*.mjs, the baseline is ${BASELINE}. Do not add fixed sleeps: wait for the state (see the waitFor* helpers in common.mjs). If you converted some, LOWER BASELINE in uat/sleeps.test.mjs to ${count}.`,
  ).toBeLessThanOrEqual(BASELINE);
});
