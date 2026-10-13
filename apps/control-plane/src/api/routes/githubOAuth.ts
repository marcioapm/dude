import { createHash, randomBytes } from "node:crypto";
import { newId } from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { config } from "../../config.ts";
import { badRequest, json, noContent } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

const hash = (value: string) => createHash("sha256").update(value).digest("hex");
const callbackPath = "/v1/me/github/callback";

function session(ctx: RequestContext) {
  if (ctx.principal.credentialKind !== "person") throw badRequest("Connect GitHub requires a logged-in person session");
  return ctx.principal;
}

function callbackUrl() {
  const auth = config().auth;
  if (auth.provider !== "cloudflare_access") throw badRequest("Connect GitHub requires Cloudflare Access sign-in");
  return auth.public_url.replace(/\/+$/, "") + callbackPath;
}

function landing(status: string) {
  return new Response(null, { status: 303, headers: {
    location: `/?github=${encodeURIComponent(status)}#/me/settings`, "cache-control": "no-store", "referrer-policy": "no-referrer",
  } });
}

export function registerGithubOAuthRoutes(router: Router, fetchImpl: typeof fetch = fetch) {
  router.get("/v1/me/github", async (ctx) => {
    const { organizationId, personId } = ctx.principal;
    const status = await withOrg(organizationId, async ({ sql }) => {
      const [credential] = await sql`SELECT oauth_client_id AS "clientId", oauth_client_secret IS NOT NULL AS configured
        FROM forge_credentials WHERE forge = 'github' AND auth = 'github_app'`;
      const [connection] = await sql`SELECT a.github_login AS login, a.expires_at AS "expiresAt",
        a.refresh_expires_at AS "refreshExpiresAt", a.reconnect_required AS "reconnectRequired"
        FROM github_user_authorizations a JOIN forge_credentials c ON c.id = a.credential_id
        WHERE a.person_id = ${personId} AND a.credential_version = c.oauth_version`;
      return { available: Boolean(credential?.clientId && credential.configured), connection: connection ?? null };
    });
    return json(status);
  });
  router.post("/v1/me/github/connect", async (ctx) => {
    const { organizationId, personId } = session(ctx);
    const state = randomBytes(32).toString("base64url");
    const verifier = randomBytes(32).toString("base64url");
    const clientId = await withOrg(organizationId, async ({ sql }) => {
      const [credential] = await sql`SELECT id, oauth_version, oauth_client_id, oauth_client_secret IS NOT NULL AS configured
        FROM forge_credentials WHERE forge = 'github' AND auth = 'github_app' FOR UPDATE`;
      if (!credential?.oauth_client_id || !credential.configured) throw badRequest("An administrator must configure GitHub App OAuth first");
      await sql`DELETE FROM github_oauth_transactions WHERE expires_at <= now()`;
      const [connection] = await sql`INSERT INTO github_oauth_connections (organization_id, person_id, credential_id)
        VALUES (${organizationId}, ${personId}, ${credential.id})
        ON CONFLICT (organization_id, person_id, credential_id) DO UPDATE
          SET generation = github_oauth_connections.generation + 1 RETURNING generation`;
      await sql`INSERT INTO github_oauth_transactions (state_hash, organization_id, person_id, credential_id, credential_version, verifier, generation)
        VALUES (${hash(state)}, ${organizationId}, ${personId}, ${credential.id}, ${credential.oauth_version}, ${verifier}, ${connection!.generation})`;
      return credential.oauth_client_id as string;
    });
    const url = new URL("https://github.com/login/oauth/authorize");
    url.search = new URLSearchParams({ client_id: clientId, redirect_uri: callbackUrl(), state, scope: "offline_access",
      code_challenge: createHash("sha256").update(verifier).digest("base64url"), code_challenge_method: "S256" }).toString();
    return json({ url: url.toString() });
  });
  router.get(callbackPath, async (ctx) => {
    try {
      const { organizationId, personId } = session(ctx);
      const state = ctx.url.searchParams.get("state") ?? "";
      const transaction = await withOrg(organizationId, async ({ sql }) => {
        const [row] = await sql`DELETE FROM github_oauth_transactions WHERE state_hash = ${hash(state)}
          AND person_id = ${personId} AND organization_id = ${organizationId}
          RETURNING credential_id, credential_version, generation, verifier, expires_at`;
        if (!row || new Date(row.expires_at).getTime() <= Date.now()) return null;
        const [credential] = await sql`SELECT oauth_client_id, oauth_client_secret, api_base_url FROM forge_credentials
          WHERE id = ${row.credential_id} AND oauth_version = ${row.credential_version} AND auth = 'github_app'`;
        return credential ? { ...row, ...credential } : null;
      });
      if (!transaction) return landing("invalid_or_expired");
      if (ctx.url.searchParams.has("error")) return landing("authorization_denied");
      const code = ctx.url.searchParams.get("code");
      if (!code) return landing("authorization_failed");
      const response = await fetchImpl("https://github.com/login/oauth/access_token", {
        method: "POST", headers: { accept: "application/json", "content-type": "application/json" },
        body: JSON.stringify({ client_id: transaction.oauth_client_id, client_secret: transaction.oauth_client_secret,
          code, code_verifier: transaction.verifier, redirect_uri: callbackUrl() }), signal: AbortSignal.timeout(10_000),
      });
      if (!response.ok) return landing("authorization_failed");
      const tokens = await response.json() as { access_token?: string; refresh_token?: string; expires_in?: number; refresh_token_expires_in?: number };
      if (!tokens.access_token || !tokens.refresh_token || !Number.isFinite(tokens.expires_in) || !Number.isFinite(tokens.refresh_token_expires_in)
        || tokens.expires_in! <= 0 || tokens.refresh_token_expires_in! <= 0) return landing("enable_expiring_tokens");
      const userResponse = await fetchImpl(`${String(transaction.api_base_url).replace(/\/+$/, "")}/user`, {
        headers: { authorization: `Bearer ${tokens.access_token}`, accept: "application/vnd.github+json" }, signal: AbortSignal.timeout(10_000),
      });
      if (!userResponse.ok) return landing("identity_failed");
      const user = await userResponse.json() as { id?: number; login?: string };
      if (!Number.isSafeInteger(user.id) || user.id! <= 0 || !user.login) return landing("identity_failed");
      const now = Date.now();
      const stored = await withOrg(organizationId, async ({ sql }) => {
        const [credential] = await sql`SELECT id FROM forge_credentials WHERE id = ${transaction.credential_id}
          AND oauth_version = ${transaction.credential_version} FOR UPDATE`;
        if (!credential) return "app_changed";
        // A later Connect or a Disconnect since this transaction started supersedes it.
        const [connection] = await sql`SELECT generation FROM github_oauth_connections
          WHERE person_id = ${personId} AND credential_id = ${credential.id} FOR UPDATE`;
        if (!connection || String(connection.generation) !== String(transaction.generation)) return "superseded";
        await sql`INSERT INTO github_user_authorizations (organization_id, person_id, credential_id, credential_version,
          github_user_id, github_login, access_token, refresh_token, expires_at, refresh_expires_at)
          VALUES (${organizationId}, ${personId}, ${credential.id}, ${transaction.credential_version}, ${user.id!}, ${user.login!},
            ${tokens.access_token!}, ${tokens.refresh_token!}, ${new Date(now + tokens.expires_in! * 1000)}, ${new Date(now + tokens.refresh_token_expires_in! * 1000)})
          ON CONFLICT (organization_id, person_id, credential_id) DO UPDATE SET
            credential_version = EXCLUDED.credential_version, github_user_id = EXCLUDED.github_user_id,
            github_login = EXCLUDED.github_login, access_token = EXCLUDED.access_token, refresh_token = EXCLUDED.refresh_token,
            expires_at = EXCLUDED.expires_at, refresh_expires_at = EXCLUDED.refresh_expires_at,
            reconnect_required = false, refresh_claim = NULL, refresh_claim_until = NULL`;
        await sql`UPDATE people SET github_login = ${user.login!}, github_user_id = ${user.id!} WHERE id = ${personId}`;
        // Mirrors workflow.SignalTx: the signal is stored even when a publication step that saw the missing
        // authorization has not committed its park yet; only the wake depends on the run already waiting.
        // Recipients are deliveries whose current branch is frozen to this person.
        const recipients = await sql`SELECT DISTINCT r.id FROM workflow_runs r JOIN pr_publications p ON p.task_id = r.task_id AND p.branch = r.state->>'branch'
          WHERE p.owner_person_id = ${personId} AND (
            (r.status = 'waiting' AND r.awaiting_signals ? 'github.authorized')
            OR (r.status = 'running' AND r.step IN ('openPullRequest', 'awaitPRFix')))`;
        for (const { id } of recipients) {
          await sql`INSERT INTO workflow_signals (id, organization_id, workflow_run_id, name, payload, idempotency_key)
            VALUES (${newId("workflowSignal")}, ${organizationId}, ${id}, 'github.authorized', ${{ personId }}::jsonb, NULL)`;
          await sql`UPDATE workflow_runs SET status = 'running', wake_at = NULL WHERE id = ${id}
            AND status = 'waiting' AND awaiting_signals ? 'github.authorized' AND (wait_until IS NULL OR wait_until <= now())`;
        }
        return "connected";
      });
      return landing(stored);
    } catch {
      return landing("authorization_failed");
    }
  });
  router.delete("/v1/me/github", async (ctx) => {
    const { organizationId, personId } = session(ctx);
    const revoked = await withOrg(organizationId, async ({ sql }) => {
      const credentials = await sql`SELECT id FROM forge_credentials WHERE forge = 'github' FOR UPDATE`;
      for (const { id } of credentials) {
        await sql`INSERT INTO github_oauth_connections (organization_id, person_id, credential_id)
          VALUES (${organizationId}, ${personId}, ${id})
          ON CONFLICT (organization_id, person_id, credential_id) DO UPDATE
            SET generation = github_oauth_connections.generation + 1`;
      }
      const [connection] = await sql`SELECT a.access_token, c.oauth_client_id, c.oauth_client_secret, c.api_base_url
        FROM github_user_authorizations a JOIN forge_credentials c ON c.id = a.credential_id
        WHERE a.person_id = ${personId} AND a.credential_version = c.oauth_version`;
      await sql`DELETE FROM github_user_authorizations WHERE person_id = ${personId}`;
      await sql`DELETE FROM github_oauth_transactions WHERE person_id = ${personId}`;
      return connection;
    });
    if (revoked?.oauth_client_id && revoked.oauth_client_secret) {
      try {
        await fetchImpl(`${String(revoked.api_base_url).replace(/\/+$/, "")}/applications/${encodeURIComponent(revoked.oauth_client_id)}/token`, {
          method: "DELETE", headers: { accept: "application/vnd.github+json", "content-type": "application/json",
            authorization: `Basic ${Buffer.from(`${revoked.oauth_client_id}:${revoked.oauth_client_secret}`).toString("base64")}` },
          body: JSON.stringify({ access_token: revoked.access_token }), signal: AbortSignal.timeout(10_000),
        });
      } catch {
        // Local invalidation has already committed; GitHub revocation is best effort.
      }
    }
    return noContent();
  });
}
