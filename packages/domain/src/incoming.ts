/**
 * Incoming webhooks: an automation's own URL, POST /v1/hooks/<hookId>, that
 * anything able to send JSON can start it through. The sender proves itself
 * with a secret (a signature, a header token or a URL parameter); the body's
 * conditions decide whether a delivery starts a run; its group key sends a
 * later delivery of the same group to the run still going.
 * docs/CONTRIBUTING.md, "Automations"; migration 211.
 */

import { z } from "zod";

export const INCOMING_VERIFY_MODES = ["signature", "header", "query"] as const;
export type IncomingVerifyMode = (typeof INCOMING_VERIFY_MODES)[number];
export const INCOMING_VERIFY_LABEL: Record<IncomingVerifyMode, string> = {
  signature: "Signature",
  header: "Header token",
  query: "URL query parameter",
};

const headerName = z.string().trim().regex(/^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,100}$/, "a header name, such as X-Signature");

export const incomingVerifySchema = z.discriminatedUnion("mode", [
  z.object({
    mode: z.literal("signature"),
    header: headerName,
    /** Before the hex digest, such as `sha256=`; empty for none. */
    prefix: z.string().max(40).regex(/^[\x21-\x7e]*$/, "printable characters, no spaces").default(""),
  }).strict(),
  z.object({ mode: z.literal("header"), header: headerName }).strict(),
  z.object({ mode: z.literal("query"), param: z.string().regex(/^[A-Za-z0-9_.-]{1,60}$/, "a parameter name, such as token") }).strict(),
]);
export type IncomingVerify = z.infer<typeof incomingVerifySchema>;

export const DEFAULT_INCOMING_VERIFY: IncomingVerify = { mode: "signature", header: "X-Signature", prefix: "sha256=" };
export const DEFAULT_VERIFY_OF: Record<IncomingVerifyMode, IncomingVerify> = {
  signature: DEFAULT_INCOMING_VERIFY,
  header: { mode: "header", header: "Authorization" },
  query: { mode: "query", param: "token" },
};

export const CONDITION_OPS = ["is", "is_not", "one_of", "contains", "exists"] as const;
export type ConditionOp = (typeof CONDITION_OPS)[number];
export const CONDITION_OP_LABEL: Record<ConditionOp, string> = {
  is: "is",
  is_not: "is not",
  one_of: "one of",
  contains: "contains",
  exists: "exists",
};

/** A dot path into a JSON body: `status`, `commonLabels.severity`, `alerts.0.status`. */
export const jsonPathSchema = z.string().trim().min(1, "a path into the body, such as status")
  .max(200).regex(/^[^.\s]+(\.[^.\s]+)*$/, "dot-separated keys, such as commonLabels.severity or alerts.0.status");

export const conditionSchema = z.object({
  path: jsonPathSchema,
  op: z.enum(CONDITION_OPS),
  values: z.array(z.string().max(500)).max(50).default([]),
}).strict().superRefine((c, ctx) => {
  const n = c.values.length;
  if (c.op === "exists" && n > 0) ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["values"], message: "exists takes no value" });
  if (c.op === "one_of" && n === 0) ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["values"], message: "one of takes at least one value" });
  if ((c.op === "is" || c.op === "is_not" || c.op === "contains") && n !== 1) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["values"], message: `${CONDITION_OP_LABEL[c.op]} takes one value` });
  }
});
export type Condition = z.infer<typeof conditionSchema>;

export const INCOMING_RATE_DEFAULT = 30;
export const INCOMING_RATE_MAX = 600;
/** The largest body the endpoint reads: 1 MiB. */
export const INCOMING_MAX_BYTES = 1_048_576;

export const hookIdSchema = z.string().regex(/^hk_[a-z0-9]{12,40}$/, "a hook id");

export const incomingTriggerSchema = z.object({
  kind: z.literal("incoming"),
  /** Its URL's last segment; set when it is created, never by a save. */
  hookId: hookIdSchema,
  verify: incomingVerifySchema.default(DEFAULT_INCOMING_VERIFY),
  conditions: z.array(conditionSchema).max(20).default([]),
  /** A path whose value groups deliveries; empty: each delivery is its own group. */
  groupBy: z.union([z.literal(""), jsonPathSchema]).default(""),
  ratePerHour: z.number().int().min(1).max(INCOMING_RATE_MAX, `at most ${INCOMING_RATE_MAX} an hour`).default(INCOMING_RATE_DEFAULT),
}).strict();
export type IncomingTrigger = z.infer<typeof incomingTriggerSchema>;

/** A new hook id: `hk_` and 16 random base-36 characters. */
export function newHookId(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return "hk_" + [...bytes].map((b) => (b % 36).toString(36)).join("");
}

// ---------------------------------------------------------------------------
// Reading a body: the orchestrator's matcher (internal/automations/incoming.go)
// reads the same way; packages/domain/test/incoming-conditions.json holds the
// cases both are tested against.
// ---------------------------------------------------------------------------

/** The value at a dot path; an all-digit segment indexes an array. Undefined when the body has none. */
export function jsonPathValue(body: unknown, path: string): unknown {
  let at: unknown = body;
  for (const key of path.split(".")) {
    if (Array.isArray(at)) {
      if (!/^\d+$/.test(key)) return undefined;
      at = at[Number(key)];
    } else if (at !== null && typeof at === "object") {
      at = Object.prototype.hasOwnProperty.call(at, key) ? (at as Record<string, unknown>)[key] : undefined;
    } else {
      return undefined;
    }
    if (at === undefined) return undefined;
  }
  return at;
}

/** A value as conditions compare it: a string as itself, anything else as its JSON. */
export function conditionText(v: unknown): string | undefined {
  if (v === undefined) return undefined;
  return typeof v === "string" ? v : JSON.stringify(v);
}

export interface ConditionResult {
  ok: boolean;
  /** What the body had at the path, as text: "missing" when it had nothing. */
  actual: string;
}

/** Whether one condition holds for a body. `contains` on an array asks whether an element is the value. */
export function conditionHolds(c: Condition, body: unknown): ConditionResult {
  const v = jsonPathValue(body, c.path);
  const text = conditionText(v);
  const actual = text ?? "missing";
  switch (c.op) {
    case "exists": return { ok: v !== undefined && v !== null, actual };
    case "is": return { ok: text !== undefined && text === c.values[0], actual };
    case "is_not": return { ok: text === undefined || text !== c.values[0], actual };
    case "one_of": return { ok: text !== undefined && c.values.includes(text), actual };
    case "contains": {
      const want = c.values[0] ?? "";
      if (Array.isArray(v)) return { ok: v.some((x) => conditionText(x) === want), actual };
      return { ok: typeof v === "string" && v.includes(want), actual };
    }
  }
}

/** A condition in words: "status is firing", "commonLabels.severity one of critical, high". */
export function describeCondition(c: Pick<Condition, "path" | "op" | "values">): string {
  return c.op === "exists" ? `${c.path} exists` : `${c.path} ${CONDITION_OP_LABEL[c.op]} ${c.values.join(", ")}`;
}

/** How much of the body's value a skip reason quotes, in characters. */
export const MISS_ACTUAL_CHARS = 200;

/** Why a missed condition skips a delivery: "commonLabels.severity is warning, not one of critical, high". The body's value is cut to MISS_ACTUAL_CHARS and "…". */
export function conditionMiss(c: Condition, r: ConditionResult): string {
  const chars = [...r.actual];
  const actual = chars.length > MISS_ACTUAL_CHARS ? chars.slice(0, MISS_ACTUAL_CHARS).join("") + "…" : r.actual;
  switch (c.op) {
    case "exists": return `${c.path} is missing`;
    case "is": return `${c.path} is ${actual}, not ${c.values[0]}`;
    case "is_not": return `${c.path} is ${actual}`;
    case "one_of": return `${c.path} is ${actual}, not one of ${c.values.join(", ")}`;
    case "contains": return `${c.path} does not contain ${c.values[0]}`;
  }
}

/** A delivery's group key: its group-by path's value as text; "" with none, or when the body has none there. */
export function groupKeyOf(groupBy: string, body: unknown): string {
  if (!groupBy) return "";
  const v = jsonPathValue(body, groupBy);
  return v === null ? "" : (conditionText(v) ?? "");
}

// ---------------------------------------------------------------------------
// Deliveries, as the API answers
// ---------------------------------------------------------------------------

export const DELIVERY_VERDICTS = ["started", "sent_to_run", "skipped", "refused"] as const;
export type DeliveryVerdictKind = (typeof DELIVERY_VERDICTS)[number];
export const REFUSAL_CODES = ["bad_signature", "not_json", "too_large", "rate_limited", "disabled"] as const;
export type RefusalCode = (typeof REFUSAL_CODES)[number];
export const REFUSAL_LABEL: Record<RefusalCode, string> = {
  bad_signature: "Bad signature",
  not_json: "Not JSON",
  too_large: "Too large",
  rate_limited: "Rate limited",
  disabled: "Turned off",
};

/**
 * One request to an incoming automation's URL, or a GitHub delivery of a
 * GitHub automation's event, with what dude did with it. A GitHub one keeps
 * Run now's fields (event, action, repository, sender, body).
 */
export interface AutomationDelivery {
  id: string;
  receivedAt: string;
  /** null until the automations loop has read it. */
  verdict: DeliveryVerdictKind | null;
  /** A skipped delivery's missed condition or filter, in words; a refused one's code. */
  reason: string | null;
  /** The condition a skipped incoming delivery missed, from 0. */
  reasonIndex: number | null;
  groupKey: string | null;
  bytes: number;
  runId: string | null;
  /** "incoming", or the GitHub event. */
  event: string;
  action: string | null;
  repository: string | null;
  sender: string | null;
}

export interface AutomationDeliveryDetail extends AutomationDelivery {
  /** As received, with every secret-bearing header's value replaced by ••••. */
  headers: Record<string, string>;
  /** null for a refused delivery, and once its 30 days are over. */
  body: Record<string, unknown> | null;
}

/** Parts of a header name that mark a credential: its value is not kept. */
const CREDENTIAL_NAME_PARTS = ["token", "secret", "signature", "cookie", "key", "auth", "password", "credential"];

/** The headers a delivery keeps no value of: the configured verification header by its exact name, and anything named like a credential (AWS's x-amz-* included). */
export function redactedHeader(name: string, verificationHeader: string | null): boolean {
  const n = name.toLowerCase();
  if (verificationHeader && n === verificationHeader.toLowerCase()) return true;
  return n.startsWith("x-amz-") || CREDENTIAL_NAME_PARTS.some((part) => n.includes(part));
}

/** The redaction mark a dropped header value is shown as. */
export const REDACTED_VALUE = "••••";
