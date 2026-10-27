/**
 * Incoming webhooks' public endpoint, POST /v1/hooks/:hookId, and the
 * Deliveries an automation's page lists (migration 211).
 *
 * The endpoint is unauthenticated, as GitHub's (/v1/webhooks/github/:org):
 * the hook's secret is the authentication. It answers at once and stores;
 * the orchestrator's automations loop matches what it stored. In order: the
 * hook is looked up (unknown: 404, nothing stored), its secret checked in
 * constant time, the body's size (1 MiB), JSON, the automation being on and
 * its hourly rate (deliveries that started a run or were sent to one, and
 * those not matched yet; skipped and refused ones take no place). A
 * refusal stores a row with its code and no body.
 */

import { createHash, createHmac, timingSafeEqual } from "node:crypto";
import {
  INCOMING_MAX_BYTES,
  newId,
  REDACTED_VALUE,
  redactedHeader,
  type AutomationDelivery,
  type AutomationDeliveryDetail,
  type IncomingVerify,
  type RefusalCode,
} from "@dude/domain";
import { withOrg, withoutTenant, type OrgScope } from "../../db/client.ts";
import { kickOrchestrator } from "../../orchestrator/client.ts";
import { json, notFound, readCapped } from "../http.ts";
import type { PublicContext, Router } from "../router.ts";

interface Hook {
  automation_id: string;
  organization_id: string;
  enabled: boolean;
  verify: IncomingVerify | null;
  secret: string | null;
  rate_per_hour: number;
}

const REFUSAL: Record<RefusalCode, { status: number; message: string }> = {
  bad_signature: { status: 401, message: "the secret does not match" },
  too_large: { status: 413, message: "the body is over 1 MiB" },
  not_json: { status: 415, message: "the body is not a JSON object" },
  disabled: { status: 403, message: "the automation is off" },
  rate_limited: { status: 429, message: "over the automation's deliveries an hour" },
};

/** Two secrets compared in constant time: their SHA-256 digests are always the same length. */
function sameSecret(given: string | null, secret: string): boolean {
  if (given === null) return false;
  const a = createHash("sha256").update(given).digest();
  const b = createHash("sha256").update(secret).digest();
  return timingSafeEqual(a, b);
}

/** HMAC-SHA256 of the raw body, hex, after the configured prefix, compared in constant time. */
export function signatureMatches(secret: string, body: Uint8Array, header: string | null, prefix: string): boolean {
  if (header === null || !header.startsWith(prefix)) return false;
  const expected = createHmac("sha256", secret).update(body).digest();
  const hex = header.slice(prefix.length).trim();
  if (!/^[0-9a-fA-F]+$/.test(hex)) return false;
  const given = Buffer.from(hex, "hex");
  return given.length === expected.length && timingSafeEqual(given, expected);
}

/** Whether a request proves itself by the hook's mode; a signature needs the whole body. */
function verified(hook: Hook, request: Request, url: URL, body: Uint8Array | null): boolean {
  const v = hook.verify;
  if (!v || !hook.secret) return false;
  switch (v.mode) {
    case "signature": return body !== null && signatureMatches(hook.secret, body, request.headers.get(v.header), v.prefix ?? "");
    case "header": {
      const raw = request.headers.get(v.header);
      // "Authorization: Bearer <secret>" and the bare secret both prove it.
      const given = raw && /^bearer\s+/i.test(raw) ? raw.replace(/^bearer\s+/i, "") : raw;
      return sameSecret(given, hook.secret);
    }
    case "query": return sameSecret(url.searchParams.get(v.param), hook.secret);
  }
}

/** The request's headers as stored: at most 50, values cut at 500 characters, secrets' values as ••••. */
export function storedHeaders(headers: Headers, verify: IncomingVerify | null): Record<string, string> {
  const named = verify && verify.mode !== "query" ? verify.header : null;
  const out: Record<string, string> = {};
  let n = 0;
  headers.forEach((value, name) => {
    if (n++ >= 50) return;
    const display = name.replace(/(^|-)([a-z])/g, (_, d: string, c: string) => d + c.toUpperCase());
    out[display] = redactedHeader(name, named) ? REDACTED_VALUE : value.slice(0, 500);
  });
  return out;
}

async function store(scope: OrgScope, hook: Hook, row: {
  id: string; bytes: number; headers: Record<string, string>; refused?: RefusalCode; payload?: unknown;
}): Promise<void> {
  await scope.sql`INSERT INTO incoming_deliveries (id, organization_id, automation_id, verdict, reason, bytes, headers, payload)
    VALUES (${row.id}, ${scope.organizationId}, ${hook.automation_id}, ${row.refused ? "refused" : null}, ${row.refused ?? null},
      ${row.bytes}, ${row.headers}::jsonb, ${row.refused ? null : row.payload}::jsonb)`;
}

async function receiveHook(ctx: PublicContext): Promise<Response> {
  const [hook] = (await withoutTenant(({ sql }) => sql`SELECT * FROM incoming_hook(${ctx.params.hookId!})`)) as Hook[];
  if (!hook) throw notFound("no incoming webhook here");
  const id = newId("incomingDelivery");
  const headers = storedHeaders(ctx.request.headers, hook.verify);
  let body: Uint8Array | null = null;
  try {
    body = await readCapped(ctx.request, INCOMING_MAX_BYTES, () => new Error("too large"));
  } catch {
    body = null;
  }
  const declared = Number(ctx.request.headers.get("content-length") ?? "0");
  const bytes = body?.length ?? (Number.isFinite(declared) && declared > INCOMING_MAX_BYTES ? declared : INCOMING_MAX_BYTES + 1);
  const refuse = async (code: RefusalCode) => {
    await withOrg(hook.organization_id, (scope) => store(scope, hook, { id, bytes, headers, refused: code }));
    const r = REFUSAL[code];
    return json({ error: { code, message: r.message }, delivery: id }, r.status);
  };
  // A signature is over the whole body: one past 1 MiB is refused as too large unchecked.
  if (body === null && hook.verify?.mode === "signature") return refuse("too_large");
  if (!verified(hook, ctx.request, ctx.url, body)) return refuse("bad_signature");
  if (body === null) return refuse("too_large");
  let payload: unknown;
  try {
    payload = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(body));
  } catch {
    return refuse("not_json");
  }
  if (payload === null || typeof payload !== "object" || Array.isArray(payload)) return refuse("not_json");
  if (!hook.enabled) return refuse("disabled");
  const accepted = await withOrg(hook.organization_id, async (scope) => {
    // One delivery at a time per automation counts the hour: two together never both take the last place.
    await scope.sql`SELECT pg_advisory_xact_lock(hashtextextended(${"incoming:" + hook.automation_id}, 0))`;
    // The hour's places go to deliveries that started a run or were sent to one. One not matched yet
    // (verdict NULL) holds a place until the loop decides, so a burst is limited before matching.
    const [{ n }] = (await scope.sql`SELECT count(*)::int AS n FROM incoming_deliveries WHERE automation_id = ${hook.automation_id}
      AND received_at > now() - interval '1 hour' AND (verdict IS NULL OR verdict IN ('started', 'sent_to_run'))`) as [{ n: number }];
    if (n >= hook.rate_per_hour) {
      await store(scope, hook, { id, bytes, headers, refused: "rate_limited" });
      return false;
    }
    await store(scope, hook, { id, bytes, headers, payload });
    return true;
  });
  if (!accepted) {
    const r = REFUSAL.rate_limited;
    return json({ error: { code: "rate_limited", message: r.message }, delivery: id }, r.status);
  }
  void kickOrchestrator(hook.organization_id);
  return json({ delivery: id }, 202);
}

// ---------------------------------------------------------------------------
// Deliveries, as the automation's page reads them
// ---------------------------------------------------------------------------

type DeliveryTrigger = { kind: "github"; event: string } | { kind: "incoming" };
const isoOf = (v: unknown) => (v instanceof Date ? v.toISOString() : (v as string));

// An incoming delivery's columns; a GitHub one's are the same names from
// webhook_deliveries and the matcher's verdict for this automation.
const INCOMING_COLUMNS = `d.id, d.received_at AS "receivedAt", d.verdict, d.reason, d.reason_index AS "reasonIndex",
  d.group_key AS "groupKey", d.bytes, d.run_id AS "runId", 'incoming' AS event, NULL AS action, NULL AS repository, NULL AS sender`;
const GITHUB_COLUMNS = `d.id, d.received_at AS "receivedAt", v.verdict, v.reason, NULL::int AS "reasonIndex", NULL AS "groupKey",
  octet_length(d.payload::text) AS bytes, v.run_id AS "runId", d.event, d.payload->>'action' AS action,
  d.payload->'repository'->>'full_name' AS repository, d.payload->'sender'->>'login' AS sender`;

function deliveryOut(r: Record<string, unknown>): AutomationDelivery {
  const { body: _b, headers: _h, ...rest } = r;
  return { ...(rest as unknown as AutomationDelivery), receivedAt: isoOf(r.receivedAt) };
}

/** The last 30 days' deliveries, newest first (at most 100); a GitHub automation's with Run now's body. */
export async function readDeliveries(scope: OrgScope, automationId: string, trigger: DeliveryTrigger): Promise<Array<AutomationDelivery & { body?: unknown }>> {
  if (trigger.kind === "incoming") {
    const rows = (await scope.sql.unsafe(`SELECT ${INCOMING_COLUMNS}, d.payload AS body FROM incoming_deliveries d
      WHERE d.automation_id = $1 AND d.received_at > now() - interval '30 days' ORDER BY d.received_at DESC, d.id DESC LIMIT 100`,
    [automationId])) as Array<Record<string, unknown>>;
    return rows.map((r) => ({ ...deliveryOut(r), body: r.body }));
  }
  const rows = (await scope.sql.unsafe(`SELECT ${GITHUB_COLUMNS}, d.payload AS body FROM webhook_deliveries d
      LEFT JOIN automation_github_verdicts v ON v.delivery_id = d.id AND v.automation_id = $1
    WHERE d.event = $2 AND d.received_at > now() - interval '30 days' ORDER BY d.received_at DESC, d.id DESC LIMIT 100`,
  [automationId, trigger.event])) as Array<Record<string, unknown>>;
  return rows.map((r) => ({ ...deliveryOut(r), body: r.body }));
}

/** One delivery with its stored headers and body; null when the automation has none of that id. */
export async function readDelivery(scope: OrgScope, automationId: string, trigger: DeliveryTrigger, id: string): Promise<AutomationDeliveryDetail | null> {
  const [r] = (trigger.kind === "incoming"
    ? await scope.sql.unsafe(`SELECT ${INCOMING_COLUMNS}, d.headers, d.payload AS body FROM incoming_deliveries d
        WHERE d.automation_id = $1 AND d.id = $2`, [automationId, id])
    : await scope.sql.unsafe(`SELECT ${GITHUB_COLUMNS}, '{}'::jsonb AS headers, d.payload AS body FROM webhook_deliveries d
        LEFT JOIN automation_github_verdicts v ON v.delivery_id = d.id AND v.automation_id = $1
      WHERE d.id = $2 AND d.event = $3`, [automationId, id, trigger.event])) as Array<Record<string, unknown>>;
  if (!r) return null;
  return { ...deliveryOut(r), headers: (r.headers ?? {}) as Record<string, string>, body: (r.body ?? null) as Record<string, unknown> | null };
}

/** A run's deliveries, oldest first: what started it, then what was sent to it. */
export async function runDeliveries(scope: OrgScope, automationId: string, runId: string): Promise<AutomationDelivery[]> {
  const rows = (await scope.sql.unsafe(`SELECT ${INCOMING_COLUMNS} FROM incoming_deliveries d
    WHERE d.automation_id = $1 AND d.run_id = $2 ORDER BY d.received_at, d.id`, [automationId, runId])) as Array<Record<string, unknown>>;
  return rows.map(deliveryOut);
}

export function registerIncomingRoutes(router: Router): void {
  router.publicRoute("POST", "/v1/hooks/:hookId", receiveHook);
}
