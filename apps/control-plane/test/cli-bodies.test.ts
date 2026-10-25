/**
 * The request bodies and queries the dude CLI's automation commands send,
 * as recorded by its Go tests (orchestrator/cmd/dude-cli/testdata/
 * automation-requests.json, regenerated with `-update`), parsed with the
 * schemas the handlers parse them with. A rejection here is a 400 or 422 the
 * CLI would get from a real server. Also ties testdata/automation-get.json,
 * the CLI's stand-in for a GET, to what the handler's shape() answers.
 *
 * No database: only schemas, and shape() on a synthetic row.
 */

import { describe, expect, test } from "bun:test";
import { join } from "node:path";
import type { AnyZodObject, ZodTypeAny } from "zod";
import {
  automationInputSchema,
  enabledSchema,
  evaluateEventSchema,
  restorePromptSchema,
  runNowSchema,
  transferOwnerSchema,
  webhookSecretSchema,
} from "@dude/domain";
import {
  COLUMNS,
  createSchema,
  evaluateBody,
  listQuery,
  runNowBody,
  runsQuery,
  scheduleNextBody,
  shape,
} from "../src/api/routes/automations.ts";
import type { RequestContext } from "../src/api/router.ts";

const RECORDED = "orchestrator/cmd/dude-cli/testdata/automation-requests.json";
const GET_FIXTURE = "orchestrator/cmd/dude-cli/testdata/automation-get.json";
const READ_ONLY = "orchestrator/cmd/dude-cli/testdata/automation-readonly.json";

interface Recorded {
  args: string[];
  route: string;
  query?: Record<string, string>;
  body?: unknown;
  scope?: "organization" | "project";
}

const repoFile = (path: string) => Bun.file(join(import.meta.dir, "../../..", path)).json();
const recorded = (await repoFile(RECORDED)) as Recorded[];
const fixture = (await repoFile(GET_FIXTURE)) as Record<string, unknown>;
const readOnly = (await repoFile(READ_ONLY)) as string[];

/** Each route's body schemas: the handler's own, and @dude/domain's where it has one too. */
const bodies: Record<string, (r: Recorded) => ZodTypeAny[]> = {
  "POST /v1/automations": () => [createSchema],
  "PUT /v1/automations/:id": (r) => [automationInputSchema(r.scope ?? "organization")],
  "PUT /v1/automations/:id/enabled": () => [enabledSchema],
  "PUT /v1/automations/:id/owner": () => [transferOwnerSchema],
  "PUT /v1/automations/:id/webhook-secret": () => [webhookSecretSchema],
  "POST /v1/automations/:id/runs": () => [runNowBody, runNowSchema],
  "POST /v1/automations/:id/test-event/evaluate": () => [evaluateBody, evaluateEventSchema],
  "POST /v1/automations/:id/prompt-history/restore": () => [restorePromptSchema],
  "POST /v1/automations/schedule/next": () => [scheduleNextBody],
};

const queries: Record<string, AnyZodObject> = {
  "GET /v1/automations": listQuery,
  "GET /v1/automations/:id/runs": runsQuery,
};

const label = (r: Recorded) => `dude ${r.args.join(" ")} → ${r.route}`;

describe("the dude CLI's automation requests", () => {
  test("were recorded", () => {
    expect(recorded.length).toBeGreaterThan(0);
  });

  test("a PUT of settings names the automation's scope", () => {
    const unscoped = recorded.filter((r) => r.route === "PUT /v1/automations/:id" && !r.scope).map(label);
    expect(unscoped).toEqual([]);
  });

  test("each body parses with its route's schemas; a route that takes none is sent none", () => {
    const refused: string[] = [];
    for (const r of recorded) {
      const schemas = bodies[r.route]?.(r);
      if (!schemas) {
        if (r.body !== undefined) refused.push(`${label(r)}: sends a body to a route that reads none`);
        continue;
      }
      for (const schema of schemas) {
        const parsed = schema.safeParse(r.body);
        if (!parsed.success) refused.push(`${label(r)}: ${JSON.stringify(parsed.error.flatten())}`);
      }
    }
    expect(refused).toEqual([]);
  });

  test("each query parses with its route's schema, every key one it reads; a route that takes none is sent none", () => {
    const refused: string[] = [];
    for (const r of recorded) {
      const schema = queries[r.route];
      if (!schema) {
        if (r.query) refused.push(`${label(r)}: sends a query to a route that reads none`);
        continue;
      }
      // The handlers strip unknown keys, so a misspelt filter is silently ignored: refuse it here.
      const parsed = schema.strict().safeParse(r.query ?? {});
      if (!parsed.success) refused.push(`${label(r)}: ${JSON.stringify(parsed.error.flatten())}`);
    }
    expect(refused).toEqual([]);
  });
});

/** The output names of a SELECT list: each item's alias, or its bare column name. */
function selectNames(list: string): string[] {
  const items: string[] = [];
  let depth = 0;
  let start = 0;
  for (let i = 0; i < list.length; i++) {
    const ch = list[i];
    if (ch === "(") depth++;
    else if (ch === ")") depth--;
    else if (ch === "," && depth === 0) {
      items.push(list.slice(start, i));
      start = i + 1;
    }
  }
  items.push(list.slice(start));
  return items.map((item) => {
    const m = /(?:\bAS\s+"?(\w+)"?|\.(\w+)|^\s*(\w+))\s*$/i.exec(item.trim());
    const name = m?.[1] ?? m?.[2] ?? m?.[3];
    if (!name) throw new Error(`cannot name the SELECT item ${item.trim()}`);
    return name;
  });
}

// automation-get.json is the CLI's stand-in for GET /v1/automations/:id; the
// documented `dude automation ID --settings | dude automation-update ID -`
// holds only while it has the server's fields and the CLI strips the read-only ones.
describe("the dude CLI's automation fixture", () => {
  test("has the keys the server's GET answers", async () => {
    const at = "2026-10-24T09:00:00.000Z";
    const row: Record<string, unknown> = Object.fromEntries(selectNames(COLUMNS).map((name) => [name, null]));
    Object.assign(row, { ownerId: "per_1", createdAt: new Date(at), updatedAt: new Date(at),
      lastRun: { id: "arn_1", status: "succeeded", at } });
    const ctx = { principal: { role: "admin", personId: "per_1" } } as unknown as RequestContext;
    const answered = Object.keys(await shape(ctx, row as unknown as Parameters<typeof shape>[1]));
    const has = Object.keys(fixture);
    expect([
      ...answered.filter((k) => !has.includes(k)).map((k) => `GET answers "${k}"; add it to ${GET_FIXTURE}, and to automationReadOnly and ${READ_ONLY} if the PUT refuses it`),
      ...has.filter((k) => !answered.includes(k)).map((k) => `${GET_FIXTURE} has "${k}", which the GET no longer answers; remove it`),
    ]).toEqual([]);
  });

  test("less the CLI's read-only fields, is what the PUT takes", () => {
    const settings = Object.fromEntries(Object.entries(fixture).filter(([k]) => !readOnly.includes(k)));
    const parsed = automationInputSchema(fixture.projectId ? "project" : "organization").safeParse(settings);
    expect(parsed.success ? [] : parsed.error.flatten()).toEqual([]);
  });
});
