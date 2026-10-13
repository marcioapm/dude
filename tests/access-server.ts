/**
 * The backend in Access mode, for the e2e suite: dude's own configuration
 * loading (`DUDE_AUTH_PROVIDER=cloudflare_access`, the team, the audience
 * and `auth.super_admins` from the environment), its startup checks, its
 * Access authenticator and every route, serving the built web app on one
 * origin. Two things stand in for Cloudflare:
 *
 * - The team's keys and identity endpoint are fetched from the suite's
 *   local edge (tests/access-edge.ts): requests for
 *   `https://<team>.cloudflareaccess.com` go to `ACCESS_EDGE_URL` instead.
 * - `auth.public_url`, which configuration requires to be https, is this
 *   server's own http://127.0.0.1 origin, so the browser's Origin header
 *   matches it; 127.0.0.1 is a secure context to the browser, as https is.
 *
 * `/cdn-cgi/access/logout` is answered as Access's edge answers it: the
 * CF_Authorization cookie is cleared and the browser sent to `/`.
 */

import { authFor, buildRouter, resolveSettings } from "../apps/control-plane/src/index.ts";
import { useConfig } from "../apps/control-plane/src/config.ts";
import { teamOrigin, type FetchLike } from "../apps/control-plane/src/api/cloudflareAccess.ts";
import { listenForEvents } from "../apps/control-plane/src/events/listen.ts";

const edge = process.env.ACCESS_EDGE_URL;
const port = Number(process.env.PORT);
if (!edge || !port) throw new Error("ACCESS_EDGE_URL and PORT are required");

const { settings, error } = resolveSettings();
if (error || !settings) throw new Error(error ?? "no settings");
if (settings.auth.provider !== "cloudflare_access") throw new Error("DUDE_AUTH_PROVIDER must be cloudflare_access");
useConfig(settings);

const team = teamOrigin(settings.auth.cloudflare_access.team);
const edgeFetch: FetchLike = (input, init) => fetch(String(input).startsWith(team) ? `${edge}${String(input).slice(team.length)}` : input, init);
const origin = `http://127.0.0.1:${port}`;
const router = buildRouter(settings.webDir, await authFor({ ...settings.auth, public_url: origin }, edgeFetch));
const stopListening = await listenForEvents(settings.databaseUrl!);

const server = Bun.serve({
  hostname: "127.0.0.1",
  port,
  idleTimeout: 0,
  fetch(request) {
    if (new URL(request.url).pathname === "/cdn-cgi/access/logout") {
      return new Response(null, { status: 302, headers: { location: "/", "set-cookie": "CF_Authorization=; Path=/; Max-Age=0" } });
    }
    return router.handle(request);
  },
});
console.log(`backend (Access) listening on ${origin}`);
process.on("SIGTERM", async () => {
  await stopListening();
  server.stop(true);
  process.exit(0);
});
