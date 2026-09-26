/**
 * Pull requests and the forge they live on.
 *
 * The orchestrator opens pull requests and keeps them in step with GitHub;
 * this is the user-facing side: listing them, storing the organization's
 * forge credential, and receiving GitHub's webhooks.
 *
 * Webhooks land here because this is the only process that serves the
 * public internet. A delivery is verified and stored, then the orchestrator
 * acts on it — so a delivery is never lost to the orchestrator being busy or
 * restarting, and GitHub always gets a fast answer.
 */

import { z } from "zod";
import { createHmac, randomBytes, timingSafeEqual } from "node:crypto";
import { newId } from "@dude/domain";
import { withOrg, withoutTenant } from "../../db/client.ts";
import { badRequest, json, notFound, parseBody, unauthorized } from "../http.ts";
import { kickOrchestrator } from "../../orchestrator/client.ts";
import type { PublicContext, RequestContext, Router } from "../router.ts";

export const PR_SELECT = `
  id, organization_id AS "organizationId", project_id AS "projectId",
  task_id AS "taskId", run_id AS "runId", repository_id AS "repositoryId",
  (SELECT name FROM repositories r WHERE r.id = pull_requests.repository_id) AS "repositoryName",
  number, node_id AS "nodeId", url, head_branch AS "headBranch",
  base_branch AS "baseBranch", head_sha AS "headSha", title, body,
  state, checks, review,
  created_at AS "createdAt", updated_at AS "updatedAt",
  merged_at AS "mergedAt", closed_at AS "closedAt"`;

async function listPullRequests(ctx: RequestContext): Promise<Response> {
  const taskId = ctx.url.searchParams.get("taskId");
  const runId = ctx.url.searchParams.get("runId");

  const pullRequests = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(PR_SELECT)} FROM pull_requests
      WHERE (${taskId}::text IS NULL OR task_id = ${taskId})
        AND (${runId}::text IS NULL OR run_id = ${runId})
      ORDER BY created_at DESC
      LIMIT 200`) as Array<Record<string, unknown>>;
  });

  return json({ pullRequests });
}

// ---------------------------------------------------------------------------
// Credential configuration
// ---------------------------------------------------------------------------

const credentialInput = z.object({
  auth: z.enum(["pat", "github_app"]).default("pat"),
  secret: z.string().min(1),
  appId: z.string().nullable().default(null),
  installationId: z.string().nullable().default(null),
  apiBaseUrl: z.string().min(1).default("https://api.github.com"),
});

interface GithubCredential {
  forge: string;
  auth: string;
  secret: string;
  appId: string | null;
  installationId: string | null;
  apiBaseUrl: string | null;
  updatedAt: string;
}

/** The organization's stored GitHub credential, secret included: for this module only. */
async function githubCredential(organizationId: string): Promise<GithubCredential | null> {
  const rows = (await withOrg(organizationId, (scope) => scope.sql`
    SELECT forge, auth, secret, app_id AS "appId", installation_id AS "installationId",
           api_base_url AS "apiBaseUrl", updated_at AS "updatedAt"
    FROM forge_credentials WHERE forge = 'github'`)) as GithubCredential[];
  return rows[0] ?? null;
}

/**
 * The organization's GitHub connection, as settings show it: how it
 * authenticates and where, never the secret — the last four characters
 * only, so a person can tell which token it is.
 */
async function getCredential(ctx: RequestContext): Promise<Response> {
  const { organizationId } = ctx.principal;
  const cred = await githubCredential(organizationId);
  if (!cred) return json({ connected: false });
  const { secret, ...shown } = cred;
  return json({ connected: true, ...shown, secretHint: secret.slice(-4), webhookPath: `/v1/webhooks/github/${organizationId}` });
}

/**
 * Ask GitHub who the stored token is, so a person can see the connection
 * works before an agent finds out it does not. Says who, and what it may do.
 */
async function verifyCredential(ctx: RequestContext): Promise<Response> {
  const cred = await githubCredential(ctx.principal.organizationId);
  if (!cred) return json({ ok: false, reason: "not connected" });
  if (cred.auth !== "pat") return json({ ok: false, reason: "only token connections can be verified yet" });
  let res: Response;
  try {
    res = await fetch(`${(cred.apiBaseUrl ?? "https://api.github.com").replace(/\/+$/, "")}/user`, {
      headers: { authorization: `Bearer ${cred.secret}`, accept: "application/vnd.github+json" },
      signal: AbortSignal.timeout(10_000),
    });
  } catch (err) {
    return json({ ok: false, reason: `GitHub did not answer: ${String(err)}` });
  }
  if (!res.ok) return json({ ok: false, reason: res.status === 401 ? "GitHub rejected the token" : `GitHub answered ${res.status}` });
  const user = (await res.json()) as { login?: string };
  return json({ ok: true, login: user.login ?? null, scopes: res.headers.get("x-oauth-scopes") });
}

/**
 * Store an organization's forge credential.
 *
 * Also mints the secret GitHub will sign webhook deliveries with, once: a
 * rotated token must not invalidate the hooks already registered with the
 * old secret. Neither secret is ever returned by any route; the response
 * says what was configured, not with what — except the webhook URL, which is
 * not a secret and is what an operator needs to register a hook by hand.
 */
async function putCredential(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, credentialInput);
  const { organizationId } = ctx.principal;

  if (input.auth === "github_app" && !(input.appId && input.installationId)) {
    throw badRequest("github_app authentication requires appId and installationId");
  }

  const saved = await withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      INSERT INTO forge_credentials (
        id, organization_id, forge, auth, secret, app_id, installation_id, api_base_url, webhook_secret)
      VALUES (${newId("forgeCredential")}, ${organizationId}, 'github', ${input.auth}::forge_auth_kind,
              ${input.secret}, ${input.appId}, ${input.installationId}, ${input.apiBaseUrl},
              ${randomBytes(32).toString("hex")})
      ON CONFLICT (organization_id, forge) DO UPDATE SET
        auth            = EXCLUDED.auth,
        secret          = EXCLUDED.secret,
        app_id          = EXCLUDED.app_id,
        installation_id = EXCLUDED.installation_id,
        api_base_url    = EXCLUDED.api_base_url,
        webhook_secret  = COALESCE(forge_credentials.webhook_secret, EXCLUDED.webhook_secret),
        updated_at      = now()
      RETURNING id, forge, auth, app_id AS "appId", installation_id AS "installationId",
                api_base_url AS "apiBaseUrl", updated_at AS "updatedAt"`) as Array<
      Record<string, unknown>
    >;
    return rows[0]!;
  });

  return json({ ...saved, webhookPath: `/v1/webhooks/github/${organizationId}` });
}

// ---------------------------------------------------------------------------
// Webhooks
// ---------------------------------------------------------------------------

/** Events that can change what dude does about a pull request. */
const RELEVANT = new Set([
  "pull_request",
  "pull_request_review",
  "pull_request_review_comment",
  "issue_comment",
  "check_suite",
  "check_run",
  "status",
]);

/** Constant-time check of GitHub's X-Hub-Signature-256 against the body. */
export function verifySignature(secret: string, body: string, header: string | null): boolean {
  if (!secret || !header?.startsWith("sha256=")) return false;
  const expected = createHmac("sha256", secret).update(body).digest();
  const given = Buffer.from(header.slice("sha256=".length), "hex");
  return given.length === expected.length && timingSafeEqual(given, expected);
}

/**
 * Receive a GitHub webhook delivery.
 *
 * Public — GitHub cannot send an API key — so the signature is the
 * authentication. The organization is in the path; its secret is looked up
 * before its tenant scope exists, which is why that one read goes around
 * row-level security, through a function that returns only the secret.
 *
 * Stored, not acted on: the orchestrator processes deliveries. GitHub's
 * delivery id is the key, so a redelivery is recognised rather than repeated.
 */
async function receiveWebhook(ctx: PublicContext): Promise<Response> {
  const organizationId = ctx.params.org!;
  const event = ctx.request.headers.get("x-github-event") ?? "";
  const deliveryId = ctx.request.headers.get("x-github-delivery") ?? "";
  const body = await ctx.request.text();

  const secret = await withoutTenant(async ({ sql }) => {
    const rows = (await sql`SELECT webhook_secret_for(${organizationId}) AS secret`) as Array<{ secret: string | null }>;
    return rows[0]?.secret ?? null;
  });
  if (!secret) throw notFound("no webhook is configured here");
  if (!verifySignature(secret, body, ctx.request.headers.get("x-hub-signature-256"))) {
    throw unauthorized("webhook signature does not match");
  }
  if (event === "ping") return json({ ok: true });
  if (!RELEVANT.has(event) || !deliveryId) return json({ ok: true, ignored: event });

  let payload: unknown;
  try {
    payload = JSON.parse(body);
  } catch {
    throw badRequest("webhook body must be JSON");
  }

  await withOrg(organizationId, async (scope) => {
    await scope.sql`
      INSERT INTO webhook_deliveries (id, organization_id, event, payload)
      VALUES (${deliveryId}, ${organizationId}, ${event}, ${payload}::jsonb)
      ON CONFLICT (id) DO NOTHING`;
  });
  void kickOrchestrator(organizationId);
  return json({ ok: true }, 202);
}

export function registerPullRequestRoutes(router: Router): void {
  router.post("/v1/forge/credential", putCredential);
  router.get("/v1/forge/credential", getCredential);
  router.post("/v1/forge/credential/verify", verifyCredential);
  router.get("/v1/pull-requests", listPullRequests);
  router.publicRoute("POST", "/v1/webhooks/github/:org", receiveWebhook);
}
