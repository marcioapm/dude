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
import { badRequest, HttpError, json, notFound, parseBody, unauthorized } from "../http.ts";
import { kickOrchestrator, orchestrator } from "../../orchestrator/client.ts";
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
  /**
   * Where GitHub reaches this dude (the browser's origin): given, dude
   * registers its webhook on every repository at once.
   */
  publicUrl: z.string().url().optional(),
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
  const webhook = await webhookHealth(organizationId);
  return json({ connected: true, ...shown, secretHint: secret.slice(-4), webhookPath: webhookPath(organizationId), webhook });
}

const webhookPath = (organizationId: string) => `/v1/webhooks/github/${organizationId}`;

/**
 * Whether GitHub's webhooks reach dude: when the last one arrived, how many
 * failed today and why the last did, and each repository's hook. A failure
 * is a delivery whose signature did not match — the secret GitHub has is not
 * dude's — or one dude could not act on.
 */
async function webhookHealth(organizationId: string) {
  return withOrg(organizationId, async (scope) => {
    const [cred] = (await scope.sql`
      SELECT webhook_last_delivery_at AS "lastDeliveryAt", webhook_last_failure_at AS "lastFailureAt",
             webhook_last_failure AS "lastFailure", webhook_rotated_at AS "rotatedAt", public_url AS "publicUrl",
             CASE WHEN webhook_failures_day = current_date THEN webhook_failures_today ELSE 0 END AS "failedToday"
      FROM forge_credentials WHERE forge = 'github'`) as Array<Record<string, unknown>>;
    const [pending] = (await scope.sql`
      SELECT count(*) FILTER (WHERE processed_at IS NULL AND attempts > 0)::int AS "retrying",
             max(last_error) FILTER (WHERE processed_at IS NULL AND attempts > 0) AS "lastError"
      FROM webhook_deliveries WHERE received_at > now() - interval '1 day'`) as Array<Record<string, unknown>>;
    const repositories = await scope.sql`
      SELECT r.id, r.name, r.url, p.name AS "projectName", r.webhook_id AS "hookId",
             r.webhook_registered_at AS "registeredAt", r.webhook_error AS "error"
      FROM repositories r JOIN projects p ON p.id = r.project_id ORDER BY p.name, r.name`;
    return { ...cred, ...pending, repositories };
  });
}

/**
 * The webhook secret, shown: a person registering a hook by hand on GitHub
 * needs it. Settings reveal it only when asked, and only this route returns
 * it.
 */
async function revealWebhookSecret(ctx: RequestContext): Promise<Response> {
  const rows = (await withOrg(ctx.principal.organizationId, (scope) => scope.sql`
    SELECT webhook_secret AS "secret" FROM forge_credentials WHERE forge = 'github'`)) as Array<{ secret: string | null }>;
  if (!rows[0]?.secret) throw notFound("GitHub is not connected");
  return json({ secret: rows[0].secret });
}

/**
 * A new webhook secret. The old one is still accepted for a day, so
 * deliveries GitHub signed before the hooks are updated are not refused;
 * dude updates the hooks it registered at once when it knows where it is
 * reached (`url`, or the one it last registered with).
 */
async function rotateWebhookSecret(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, z.object({ url: z.string().url().optional() }));
  const { organizationId } = ctx.principal;
  const rows = (await withOrg(organizationId, (scope) => scope.sql`
    UPDATE forge_credentials SET previous_webhook_secret = webhook_secret, webhook_secret = ${randomBytes(32).toString("hex")},
      webhook_rotated_at = now(), updated_at = now()
    WHERE forge = 'github' RETURNING webhook_secret AS "secret", public_url AS "publicUrl"`)) as Array<{
    secret: string;
    publicUrl: string | null;
  }>;
  if (!rows[0]) throw notFound("GitHub is not connected");
  const url = input.url ?? rows[0].publicUrl;
  const registered = url ? await register(ctx, url) : null;
  return json({ secret: rows[0].secret, registered });
}

const registerInput = z.object({
  /** Where GitHub reaches this dude, as the person asking reaches it. */
  url: z.string().url(),
  repositoryId: z.string().min(1).optional(),
});

/** Register dude's webhook on the organization's repositories (or one). */
async function registerWebhooks(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, registerInput);
  return json(await register(ctx, input.url, input.repositoryId));
}

async function register(ctx: RequestContext, base: string, repositoryId?: string): Promise<unknown> {
  const { organizationId } = ctx.principal;
  const origin = base.replace(/\/+$/, "");
  await withOrg(organizationId, (scope) => scope.sql`
    UPDATE forge_credentials SET public_url = ${origin} WHERE forge = 'github'`);
  const res = await orchestrator(organizationId, "POST", "/internal/webhooks/register",
    JSON.stringify({ url: `${origin}${webhookPath(organizationId)}`, ...(repositoryId ? { repositoryId } : {}) }),
    ctx.principal.apiKeyId);
  const body = (await res.json()) as { error?: { message?: string; code?: string } };
  if (!res.ok) throw new HttpError(res.status, body.error?.message ?? "registering webhooks failed", body.error?.code ?? "error");
  return body;
}

/**
 * A repository just added: its webhook is registered where the
 * organization's others were, if dude knows where that is. Best effort —
 * the repository is added either way, and settings say if its hook is not.
 */
export async function registerRepositoryWebhook(ctx: RequestContext, repositoryId: string): Promise<void> {
  try {
    const rows = (await withOrg(ctx.principal.organizationId, (scope) => scope.sql`
      SELECT public_url AS "publicUrl" FROM forge_credentials WHERE forge = 'github' AND public_url IS NOT NULL`)) as Array<{
      publicUrl: string;
    }>;
    if (rows[0]) await register(ctx, rows[0].publicUrl, repositoryId);
  } catch {
    // Recorded on the repository by the orchestrator where it got that far.
  }
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

  // Registering is a courtesy on connect: the token is saved whether or
  // not GitHub lets it add hooks, and settings say which it could not.
  let registered: unknown = null;
  if (input.publicUrl) {
    try {
      registered = await register(ctx, input.publicUrl);
    } catch (err) {
      registered = { error: err instanceof Error ? err.message : String(err) };
    }
  }
  return json({ ...saved, webhookPath: webhookPath(organizationId), registered });
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

  const secrets = await withoutTenant(async ({ sql }) => {
    const rows = (await sql`SELECT webhook_secrets_for(${organizationId}) AS secrets`) as Array<{ secrets: string[] | null }>;
    return rows[0]?.secrets ?? [];
  });
  if (secrets.length === 0) throw notFound("no webhook is configured here");
  const signature = ctx.request.headers.get("x-hub-signature-256");
  // The current secret, or for a day after a rotation the one before it.
  const signed = secrets.some((secret) => verifySignature(secret, body, signature));
  await noteDelivery(organizationId, signed ? null : "signature did not match: the secret GitHub has is not dude's");
  if (!signed) throw unauthorized("webhook signature does not match");
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

/** When a delivery arrived, and whether it was signed: the webhook's health. */
async function noteDelivery(organizationId: string, failure: string | null): Promise<void> {
  await withoutTenant(({ sql }) => sql`SELECT note_webhook_delivery(${organizationId}, ${failure})`);
}

export function registerPullRequestRoutes(router: Router): void {
  router.get("/v1/forge/webhook-secret", revealWebhookSecret);
  router.post("/v1/forge/webhook-secret/rotate", rotateWebhookSecret);
  router.post("/v1/forge/webhooks/register", registerWebhooks);
  router.post("/v1/forge/credential", putCredential);
  router.get("/v1/forge/credential", getCredential);
  router.post("/v1/forge/credential/verify", verifyCredential);
  router.get("/v1/pull-requests", listPullRequests);
  router.publicRoute("POST", "/v1/webhooks/github/:org", receiveWebhook);
}
