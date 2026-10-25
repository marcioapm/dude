/**
 * The request bodies and queries the dude CLI's automation commands send,
 * as recorded by its Go tests (orchestrator/cmd/dude-cli/testdata/
 * automation-requests.json, regenerated with `-update`), parsed with the
 * schemas the handlers parse them with. A rejection here is a 400 or 422 the
 * CLI would get from a real server.
 *
 * No database: only schemas.
 */

import { describe, expect, test } from "bun:test";
import { join } from "node:path";
import type { ZodTypeAny } from "zod";
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
  createSchema,
  evaluateBody,
  listQuery,
  runNowBody,
  runsQuery,
  scheduleNextBody,
} from "../src/api/routes/automations.ts";

const RECORDED = "orchestrator/cmd/dude-cli/testdata/automation-requests.json";

interface Recorded {
  args: string[];
  route: string;
  query?: Record<string, string>;
  body?: unknown;
  scope?: "organization" | "project";
}

const recorded = (await Bun.file(join(import.meta.dir, "../../..", RECORDED)).json()) as Recorded[];

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

const queries: Record<string, ZodTypeAny> = {
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

  test("each query parses with its route's schema; a route that takes none is sent none", () => {
    const refused: string[] = [];
    for (const r of recorded) {
      const schema = queries[r.route];
      if (!schema) {
        if (r.query) refused.push(`${label(r)}: sends a query to a route that reads none`);
        continue;
      }
      const parsed = schema.safeParse(r.query ?? {});
      if (!parsed.success) refused.push(`${label(r)}: ${JSON.stringify(parsed.error.flatten())}`);
    }
    expect(refused).toEqual([]);
  });
});
