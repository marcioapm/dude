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
  type DeliveryFilter,
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

/** How many refused rows an automation stores in a clock hour; past it a refusal is only counted (incoming_refusals). */
export const REFUSED_ROWS_PER_HOUR = 100;

async function store(scope: OrgScope, hook: Hook, row: {
  id: string; bytes: number; headers: Record<string, string>; refused?: RefusalCode; payload?: unknown;
}): Promise<void> {
  await scope.sql`INSERT INTO incoming_deliveries (id, organization_id, automation_id, verdict, reason, bytes, headers, payload)
    VALUES (${row.id}, ${scope.organizationId}, ${hook.automation_id}, ${row.refused ? "refused" : null}, ${row.refused ?? null},
      ${row.bytes}, ${row.headers}::jsonb, ${row.refused ? null : row.payload}::jsonb)`;
}

/** A refusal's row, or past REFUSED_ROWS_PER_HOUR this clock hour only its count: a flood without the secret writes one row an hour. */
async function storeRefusal(scope: OrgScope, hook: Hook, row: { id: string; bytes: number; headers: Record<string, string>; refused: RefusalCode }): Promise<void> {
  await scope.sql`SELECT pg_advisory_xact_lock(hashtextextended(${"incoming-refused:" + hook.automation_id}, 0))`;
  const [{ n }] = (await scope.sql`SELECT count(*)::int AS n FROM (SELECT 1 FROM incoming_deliveries WHERE automation_id = ${hook.automation_id}
    AND verdict = 'refused' AND received_at >= date_trunc('hour', now()) LIMIT ${REFUSED_ROWS_PER_HOUR}) r`) as [{ n: number }];
  if (n < REFUSED_ROWS_PER_HOUR) return store(scope, hook, row);
  await scope.sql`INSERT INTO incoming_refusals (organization_id, automation_id, hour, code, n)
    VALUES (${scope.organizationId}, ${hook.automation_id}, date_trunc('hour', now()), ${row.refused}, 1)
    ON CONFLICT (automation_id, hour, code) DO UPDATE SET n = incoming_refusals.n + 1`;
}

/** The refusals this clock hour that were counted, not stored. */
export async function refusedNotStored(scope: OrgScope, automationId: string): Promise<number> {
  const [{ n }] = (await scope.sql`SELECT COALESCE(sum(n), 0)::int AS n FROM incoming_refusals
    WHERE automation_id = ${automationId} AND hour = date_trunc('hour', now())`) as [{ n: number }];
  return n;
}

/** How deep a body may nest: the jsonb insert serialises and parses it recursively. */
export const INCOMING_MAX_DEPTH = 512;

/**
 * Whether a parsed body cannot be stored as jsonb: a NUL in a string or key
 * (JSON allows `\u0000`, jsonb refuses it), or nesting past INCOMING_MAX_DEPTH.
 * Walked with two parallel stacks (value, depth) rather than recursion, so the check
 * never overflows, and allocates no tuple per element: a 1 MiB array is ~520k of them.
 */
export function unstorable(body: unknown): boolean {
  const values: unknown[] = [body];
  const depths: number[] = [1];
  while (values.length > 0) {
    const v = values.pop();
    const depth = depths.pop()!;
    if (typeof v === "string") {
      if (v.includes("\u0000")) return true;
    } else if (v !== null && typeof v === "object") {
      if (depth > INCOMING_MAX_DEPTH) return true;
      if (Array.isArray(v)) {
        for (const x of v) {
          values.push(x);
          depths.push(depth + 1);
        }
      } else {
        for (const k in v) {
          if (k.includes("\u0000")) return true;
          values.push((v as Record<string, unknown>)[k]);
          depths.push(depth + 1);
        }
      }
    }
  }
  return false;
}

async function receiveHook(ctx: PublicContext): Promise<Response> {
  const [hook] = (await withoutTenant(({ sql }) => sql`SELECT * FROM incoming_hook(${ctx.params.hookId!})`)) as Hook[];
  if (!hook) throw notFound("no incoming webhook here");
  const id = newId("incomingDelivery");
  // The declared length is the sender's word: a stored size is at most one past the cap.
  const declared = Math.min(Number(ctx.request.headers.get("content-length")) || 0, INCOMING_MAX_BYTES + 1);
  let body: Uint8Array | null = null;
  let bytes = 0;
  const refused = (code: RefusalCode) => json({ error: { code, message: REFUSAL[code].message }, delivery: id }, REFUSAL[code].status);
  const refuse = async (code: RefusalCode) => {
    // A request that did not prove itself keeps no headers: nothing it sent is stored but its size.
    const headers = code === "bad_signature" ? {} : storedHeaders(ctx.request.headers, hook.verify);
    await withOrg(hook.organization_id, (scope) => storeRefusal(scope, hook, { id, bytes, headers, refused: code }));
    return refused(code);
  };
  const read = async () => {
    try {
      body = await readCapped(ctx.request, INCOMING_MAX_BYTES, () => new Error("too large"));
    } catch {
      body = null;
    }
    bytes = body?.length ?? INCOMING_MAX_BYTES + 1;
  };
  if (hook.verify?.mode === "signature") {
    // A signature is over the whole body: one past 1 MiB is refused as too large unchecked.
    await read();
    if (body === null) return refuse("too_large");
    if (!verified(hook, ctx.request, ctx.url, body)) return refuse("bad_signature");
  } else {
    // A header or query secret is checked before the body is read: a request without it costs no read.
    bytes = Math.max(declared, 0);
    if (!verified(hook, ctx.request, ctx.url, null)) return refuse("bad_signature");
    await read();
    if (body === null) return refuse("too_large");
  }
  const headers = storedHeaders(ctx.request.headers, hook.verify);
  let payload: unknown;
  try {
    payload = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(body!));
  } catch {
    return refuse("not_json");
  }
  if (payload === null || typeof payload !== "object" || Array.isArray(payload) || unstorable(payload)) return refuse("not_json");
  if (!hook.enabled) return refuse("disabled");
  const accepted = await withOrg(hook.organization_id, async (scope) => {
    // One delivery at a time per automation counts the hour: two together never both take the last place.
    await scope.sql`SELECT pg_advisory_xact_lock(hashtextextended(${"incoming:" + hook.automation_id}, 0))`;
    // The hour's places go to deliveries that started a run or were sent to one. One not matched yet
    // (verdict NULL) holds a place until the loop decides, so a burst is limited before matching.
    const [{ n }] = (await scope.sql`SELECT count(*)::int AS n FROM incoming_deliveries WHERE automation_id = ${hook.automation_id}
      AND received_at > now() - interval '1 hour' AND (verdict IS NULL OR verdict IN ('started', 'sent_to_run'))`) as [{ n: number }];
    if (n >= hook.rate_per_hour) {
      await storeRefusal(scope, hook, { id, bytes, headers, refused: "rate_limited" });
      return false;
    }
    await store(scope, hook, { id, bytes, headers, payload });
    return true;
  });
  if (!accepted) return refused("rate_limited");
  void kickOrchestrator(hook.organization_id);
  return json({ delivery: id }, 202);
}

// ---------------------------------------------------------------------------
// Deliveries, as the automation's page reads them
// ---------------------------------------------------------------------------

type DeliveryTrigger = { kind: "github"; event: string } | { kind: "incoming" };
const isoOf = (v: unknown) => (v instanceof Date ? v.toISOString() : (v as string));

// An incoming delivery's columns; a GitHub one's are the same names from
// webhook_deliveries and the matcher's verdict for this automation (a
// delivery stored before bytes existed is measured, until it ages out).
const INCOMING_COLUMNS = `d.id, d.received_at AS "receivedAt", d.verdict, d.reason, d.reason_index AS "reasonIndex",
  d.group_key AS "groupKey", d.bytes, d.run_id AS "runId", 'incoming' AS event, NULL AS action, NULL AS repository, NULL AS sender`;
const GITHUB_COLUMNS = `d.id, d.received_at AS "receivedAt", v.verdict, v.reason, NULL::int AS "reasonIndex", NULL AS "groupKey",
  COALESCE(d.bytes, octet_length(d.payload::text)) AS bytes, v.run_id AS "runId", d.event, d.payload->>'action' AS action,
  d.payload->'repository'->>'full_name' AS repository, d.payload->'sender'->>'login' AS sender`;

function deliveryOut(r: Record<string, unknown>): AutomationDelivery {
  const { body: _b, headers: _h, ...rest } = r;
  return { ...(rest as unknown as AutomationDelivery), receivedAt: isoOf(r.receivedAt) };
}

/**
 * The last 30 days' deliveries, newest first (at most 100), of one verdict
 * when filtered: on the server, so a burst of refusals hides none of the
 * rest. No bodies: a delivery's detail (readDelivery) has its body.
 */
export async function readDeliveries(scope: OrgScope, automationId: string, trigger: DeliveryTrigger, verdict: DeliveryFilter | null = null): Promise<AutomationDelivery[]> {
  if (trigger.kind === "incoming") {
    const rows = (await scope.sql.unsafe(`SELECT ${INCOMING_COLUMNS} FROM incoming_deliveries d
      WHERE d.automation_id = $1 AND d.received_at > now() - interval '30 days'
        AND ($2::text IS NULL OR d.verdict = $2 OR ($2 = 'pending' AND d.verdict IS NULL)
          OR ($2 = 'accepted' AND d.verdict IS DISTINCT FROM 'refused'))
      ORDER BY d.received_at DESC, d.id DESC LIMIT 100`, [automationId, verdict])) as Array<Record<string, unknown>>;
    return rows.map(deliveryOut);
  }
  const rows = (await scope.sql.unsafe(`SELECT ${GITHUB_COLUMNS} FROM webhook_deliveries d
      LEFT JOIN automation_github_verdicts v ON v.delivery_id = d.id AND v.automation_id = $1
    WHERE d.event = $2 AND d.received_at > now() - interval '30 days'
      AND ($3::text IS NULL OR v.verdict = $3 OR ($3 IN ('pending', 'accepted') AND v.verdict IS NULL)
        OR ($3 = 'accepted' AND v.verdict IS NOT NULL))
    ORDER BY d.received_at DESC, d.id DESC LIMIT 100`, [automationId, trigger.event, verdict])) as Array<Record<string, unknown>>;
  return rows.map(deliveryOut);
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
