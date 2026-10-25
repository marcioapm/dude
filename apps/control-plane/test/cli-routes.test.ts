/**
 * Every authenticated /v1/ route has a decision about the dude CLI:
 * orchestrator/cmd/dude-cli/routes.json names the command that calls it, or
 * says why people use the web app or `dude api` instead. The CLI's Go tests
 * check the other half: each command calls only the routes mapped to it.
 *
 * No database: the router is built with a stub authenticator and never
 * handles a request.
 */

import { describe, expect, test } from "bun:test";
import { join } from "node:path";
import { buildRouter } from "../src/index.ts";

const MANIFEST = "orchestrator/cmd/dude-cli/routes.json";

const NOT_IN_CLI_YET = "not in the CLI yet: the web app or `dude api`";

// The only apiOnly reasons: a new one is an edit here, which review sees.
const REASONS = new Set([
  "admin configuration: the settings UI or `dude api`",
  "super-admin: the admin console or `dude api`",
  "personal account: Your settings in the web app, or `dude api`",
  "browser-only: the web app's own plumbing (uploads, downloads, live streams, pickers, badges, push)",
  "browser sign-in flow: an OAuth or app-install redirect a terminal cannot complete",
  "machine-to-machine: agents, harnesses and test fixtures call it, not people",
  "retired: answers 410 Gone",
  NOT_IN_CLI_YET,
]);

// The backlog of person-facing routes without a command. It only shrinks:
// a route outside it may not use NOT_IN_CLI_YET.
const NOT_YET: ReadonlySet<string> = new Set([
  "DELETE /v1/runs/:runId/servers/:name",
  "DELETE /v1/sessions/:id/agent-machine",
  "GET /v1/brainstorms/open",
  "GET /v1/runs/:runId/servers",
  "GET /v1/runs/:runId/servers/:name/log",
  "PATCH /v1/feedback/:id",
  "PATCH /v1/memory/memories/:id",
  "POST /v1/brainstorms/:id/accept",
  "POST /v1/brainstorms/:id/decline",
  "POST /v1/brainstorms/:id/link",
  "POST /v1/brainstorms/:id/model",
  "POST /v1/brainstorms/:id/open",
  "POST /v1/brainstorms/:id/owner",
  "POST /v1/brainstorms/:id/people/:person/remove",
  "POST /v1/brainstorms/:id/people/:person/role",
  "POST /v1/brainstorms/:id/take-ownership",
  "POST /v1/runs/:runId/servers",
  "POST /v1/runs/:runId/servers/:name/restart",
  "POST /v1/runs/:runId/servers/:name/start",
  "POST /v1/runs/:runId/servers/:name/stop",
  "POST /v1/runs/:runId/servers/start-all",
  "POST /v1/runs/:runId/servers/stop-all",
  "PUT /v1/brainstorms/:id/access",
]);

type Entry = string | string[] | { apiOnly: string };

const manifest = (await Bun.file(join(import.meta.dir, "../../..", MANIFEST)).json()) as Record<string, Entry>;
const registered = buildRouter("", async () => null)
  .routes()
  .filter((r) => !r.public && r.pattern.startsWith("/v1/"))
  .map((r) => `${r.method} ${r.pattern}`);

describe("the dude CLI's route manifest", () => {
  test("names every authenticated /v1/ route", () => {
    const missing = registered.filter((key) => !(key in manifest)).map((key) =>
      `"${key}" has no entry in ${MANIFEST}: add the dude CLI command that calls it (see docs/cli.md, "Adding a command"), ` +
      `or {"apiOnly": "<why people use the UI or dude api instead>"}`);
    expect(missing).toEqual([]);
  });

  test("names no route that is gone", () => {
    const known = new Set(registered);
    const stale = Object.keys(manifest).filter((key) => !known.has(key)).map((key) =>
      `stale entry "${key}" in ${MANIFEST}: no such route is registered; remove it (and its CLI command if nothing else uses it)`);
    expect(stale).toEqual([]);
  });

  test("each entry is a command, a list of commands, or a reason", () => {
    const bad = Object.entries(manifest).flatMap(([key, v]) => {
      if (typeof v === "string") return v.trim() ? [] : [`"${key}": an empty command name`];
      if (Array.isArray(v)) {
        return v.length > 0 && v.every((n) => typeof n === "string" && n.trim()) ? [] : [`"${key}": a list of command names, none empty`];
      }
      if (v && typeof v === "object" && typeof v.apiOnly === "string") {
        return v.apiOnly.trim() ? [] : [`"${key}": an empty apiOnly reason; say why people use the UI or dude api instead`];
      }
      return [`"${key}": a command name, a list of them, or {"apiOnly": "<why>"}`];
    });
    expect(bad).toEqual([]);
  });

  test("its keys are sorted, so a new route lands in one obvious place", () => {
    const keys = Object.keys(manifest);
    expect(keys).toEqual([...keys].sort());
  });

  test("an apiOnly reason is one of the fixed set", () => {
    const bad = Object.entries(manifest).flatMap(([key, v]) =>
      typeof v === "object" && !Array.isArray(v) && !REASONS.has(v.apiOnly)
        ? [`"${key}": apiOnly reason ${JSON.stringify(v.apiOnly)} is none of the reasons in cli-routes.test.ts; reuse one of them`]
        : []);
    expect(bad).toEqual([]);
  });

  test(`"not in the CLI yet" is closed to new routes, and NOT_YET only shrinks`, () => {
    const notYet = (key: string) => {
      const v = manifest[key];
      return typeof v === "object" && !Array.isArray(v) && v.apiOnly === NOT_IN_CLI_YET;
    };
    const known = new Set(registered);
    const added = Object.keys(manifest).filter((key) => notYet(key) && !NOT_YET.has(key)).map((key) =>
      `"${key}" is marked "not in the CLI yet"; that reason is closed to new routes: add the dude CLI command ` +
      `(docs/cli.md, "Adding a command"), or give one of the other reasons`);
    const done = [...NOT_YET].filter((key) => !notYet(key) || !known.has(key)).map((key) =>
      `remove "${key}" from NOT_YET in cli-routes.test.ts`);
    expect([...added, ...done]).toEqual([]);
  });
});
