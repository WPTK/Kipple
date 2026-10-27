// UAT: the unread dot of a badge keeps its colour under forced colors (Windows High Contrast).
//
//   npm run seed                          in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/unread-dot-forced.mjs        in another
//   node uat/unread-dot-forced.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812), with "Unread badge" set to Dot only and forced colors on:
//   D1  every unread count badge dot in the navigation is drawn in CanvasText, the
//       text color, so it is not removed by the browser's forced palette.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { expect } from "@playwright/test";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 9);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;

  await step(`${tag} D1`, async () => {
    await page.goto("/settings/appearance", { waitUntil: "load" });
    const dotOnly = page.getByRole("radio", { name: "Dot only", exact: true });
    await dotOnly.waitFor({ timeout: 15000 });
    await page.getByText("Dot only", { exact: true }).click();
    await expect(dotOnly).toBeChecked();
    await page.emulateMedia({ forcedColors: "active" });
    await page.goto("/feeds", { waitUntil: "load" });
    const dots = page.locator('[data-testid="unread-dot"]');
    await dots.first().waitFor({ state: "attached", timeout: 15000 });
    const seen = await dots.evaluateAll((els) =>
      els.map((el) => {
        const probe = document.createElement("span");
        probe.style.color = "CanvasText";
        document.body.append(probe);
        const want = getComputedStyle(probe).color;
        probe.remove();
        return { got: getComputedStyle(el).backgroundColor, want };
      }),
    );
    const bad = seen.filter((s) => s.got !== s.want);
    check(seen.length > 0 && bad.length === 0, `${tag} D1 color`, "every unread dot is drawn in CanvasText under forced colors", `${bad.length} of ${seen.length} differ, e.g. ${JSON.stringify(bad[0])}`);
    await t.shot(page, `${tag}-unread-dot-forced`);
  });
});

await t.finish("unread dot, forced colors");
