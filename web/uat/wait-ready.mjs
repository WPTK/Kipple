// Waits until a freshly seeded instance has fetched its feeds (some feed has unread articles), so the UAT flows that
// need articles can start. `npm run seed` returns once the server answers; the feeds arrive over the next minute.
//
//   node uat/wait-ready.mjs [--url http://127.0.0.1:1919] [--seconds 240]
//
// Exit code: 0 ready, 2 the instance never had articles (the seed's feeds need network access). Loopback only.
import { parseArgs } from "node:util";

const { values } = parseArgs({
  options: {
    url: { type: "string", default: process.env.KIPPLE_UAT_URL || "http://127.0.0.1:1919" },
    seconds: { type: "string", default: "240" },
  },
});
const origin = new URL(values.url).origin;
if (!["127.0.0.1", "[::1]"].includes(new URL(origin).hostname)) {
  console.error("only a loopback --url");
  process.exit(2);
}
// The seed's throwaway local credentials (web/scripts/seed.mjs), not a secret.
const login = await fetch(`${origin}/api/auth/login`, {
  method: "POST",
  headers: { "Content-Type": "application/json", "X-Kipple-Client": "web", Origin: origin },
  body: JSON.stringify({ username: "dev", password: "dev-password-only-for-local-testing" }),
});
const cookie = login.headers.getSetCookie().map((c) => c.split(";")[0]).join("; ");
if (!login.ok || !cookie) {
  console.error(`sign-in failed (${login.status})`);
  process.exit(2);
}
const end = Date.now() + Number(values.seconds) * 1000;
while (Date.now() < end) {
  const b = await fetch(`${origin}/api/bootstrap`, { headers: { Cookie: cookie, Accept: "application/json" } }).then((r) => r.json());
  const unread = (b.feeds ?? []).filter((f) => !f.is_archive).reduce((n, f) => n + (f.unread ?? 0), 0);
  if (unread > 0) {
    console.log(`ready: ${unread} unread articles`);
    process.exit(0);
  }
  await new Promise((r) => setTimeout(r, 3000));
}
console.error("no feed had articles in time (the seed's feeds need network access)");
process.exit(2);
