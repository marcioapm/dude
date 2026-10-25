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
});
