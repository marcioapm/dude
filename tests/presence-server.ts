// Test-only person-session injection, not Cloudflare signature verification.
import { authenticate, personPrincipal } from "../apps/control-plane/src/api/auth.ts";
import { buildRouter } from "../apps/control-plane/src/index.ts";
import { listenForEvents } from "../apps/control-plane/src/events/listen.ts";

const designation = process.env.PRESENCE_TEST_DESIGNATION!;
if (!designation) throw new Error("PRESENCE_TEST_DESIGNATION is required");
const sessions = new Map<string, string>();
const router = buildRouter("apps/web/dist", async (credential, request) => {
  const token = request.headers.get("cookie")?.split(";").map((s) => s.trim())
    .find((s) => s.startsWith("presence_test_session="))?.slice("presence_test_session=".length);
  const key = token ? sessions.get(token) : undefined;
  if (key) {
    const principal = await authenticate(`Bearer ${key}`);
    return principal ? personPrincipal(principal.organizationId, principal.personId) : null;
  }
  return credential === null ? null : authenticate(credential);
});
const stopListening = await listenForEvents(process.env.DATABASE_URL!);
const server = Bun.serve({ hostname: "127.0.0.1", port: Number(process.env.PORT), idleTimeout: 0,
  async fetch(request) {
    if (new URL(request.url).pathname === "/test/presence-session" && request.method === "POST") {
      if (request.headers.get("x-test-designation") !== designation) return new Response(null, { status: 403 });
      const credential = request.headers.get("authorization");
      const principal = credential ? await authenticate(credential) : null;
      if (!principal) return new Response(null, { status: 401 });
      const token = crypto.randomUUID();
      sessions.set(token, credential!.replace(/^Bearer /, ""));
      return new Response(null, { status: 204, headers: {
        "set-cookie": `presence_test_session=${token}; HttpOnly; SameSite=Strict; Path=/`,
      } });
    }
    return router.handle(request);
  },
});
process.on("SIGTERM", async () => { await stopListening(); server.stop(true); process.exit(0); });
