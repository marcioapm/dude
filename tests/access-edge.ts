/**
 * A stand-in for a Cloudflare Access team, for the e2e suite's Access mode:
 * the team's published keys (`/cdn-cgi/access/certs`) and identity endpoint
 * (`/cdn-cgi/access/get-identity`), served over HTTP on a local port, and a
 * way for the suite to have a token signed for an email. Tokens are signed
 * as the control plane's own Access tests sign them
 * (apps/control-plane/test/access-harness.ts: RS256, this team's issuer and
 * audience, `type: "app"`), with a key only this process holds.
 *
 * `POST /test/token {email, name?, picture?}` answers `{token}` and
 * remembers the profile get-identity gives for that email. It needs the
 * `x-test-designation` the suite started this process with.
 */

import { rsaKey, sign } from "../apps/control-plane/test/access-harness.ts";

/** The email a token names; the edge only reads its own tokens, so no check is needed here. */
function emailIn(token: string): string {
  const payload = JSON.parse(Buffer.from(token.split(".")[1] ?? "", "base64url").toString("utf8")) as { email?: unknown };
  return String(payload.email ?? "").toLowerCase();
}

const designation = process.env.ACCESS_EDGE_DESIGNATION;
if (!designation) throw new Error("ACCESS_EDGE_DESIGNATION is required");
const key = await rsaKey(`e2e-${crypto.randomUUID().slice(0, 8)}`);
const profiles = new Map<string, { email: string; name?: string; oidc_fields?: { picture: string } }>();

function cookieToken(request: Request): string | null {
  for (const part of (request.headers.get("cookie") ?? "").split(";")) {
    const [name, ...value] = part.trim().split("=");
    if (name === "CF_Authorization") return value.join("=");
  }
  return null;
}

const server = Bun.serve({
  hostname: "127.0.0.1",
  port: Number(process.env.PORT ?? 0),
  async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/health") return new Response("ok");
    if (url.pathname === "/cdn-cgi/access/certs") return Response.json({ keys: [key.jwk] });
    if (url.pathname === "/cdn-cgi/access/get-identity") {
      const token = cookieToken(request);
      if (!token) return new Response(null, { status: 401 });
      const email = emailIn(token);
      return Response.json(profiles.get(email) ?? { email });
    }
    if (url.pathname === "/test/token" && request.method === "POST") {
      if (request.headers.get("x-test-designation") !== designation) return new Response(null, { status: 403 });
      const body = (await request.json()) as { email: string; name?: string; picture?: string };
      const email = body.email.toLowerCase();
      profiles.set(email, { email, ...(body.name ? { name: body.name } : {}), ...(body.picture ? { oidc_fields: { picture: body.picture } } : {}) });
      const now = Math.floor(Date.now() / 1000);
      return Response.json({ token: await sign(key, { email: body.email }, { exp: now + 4 * 3600 }) });
    }
    return new Response("not here", { status: 404 });
  },
});
console.log(`access edge on ${server.port}`);
if (process.env.ACCESS_EDGE_ADDR_FILE) await Bun.write(process.env.ACCESS_EDGE_ADDR_FILE, `127.0.0.1:${server.port}`);
process.on("SIGTERM", () => {
  server.stop(true);
  process.exit(0);
});
