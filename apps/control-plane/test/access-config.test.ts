import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createApiKey } from "../src/api/auth.ts";
import { Config, ConfigError } from "../src/config.ts";
import { authFor, routerFor as backendRouter } from "../src/index.ts";
import { closePool, setPool } from "../src/db/client.ts";
import { AUD, Edge, ORIGIN, TEAM, accessConfig, cookie, databases, me, member, rsaKey, sign, type Key } from "./access-harness.ts";

const suffix = Bun.randomUUIDv7("hex").slice(-12);
const org = `org_access_config_${suffix}`;
let owner: SQL;
let app: SQL;
let signer: Key;

beforeAll(async () => {
  ({ owner, app } = databases());
  setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  signer = await rsaKey("k1");
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${org}`;
  await owner`DELETE FROM accounts WHERE email LIKE ${`%@${suffix}.config.test`}`;
  await owner.end();
});

describe("configuration", () => {
  const valid = `
[auth]
provider = "cloudflare_access"
public_url = "https://dude.acme.dev"
super_admins = ["Ops@Acme.dev"]
[auth.cloudflare_access]
team = "acme"
aud = "application-audience"
`;
  const dir = mkdtempSync(join(tmpdir(), "dude-auth-config-"));
  afterAll(() => rmSync(dir, { recursive: true, force: true }));
  let files = 0;
  // The settings the backend resolves from `text` as its file, with `env` over it.
  const loaded = (text: string, env: Record<string, string> = {}) => {
    const path = join(dir, `${++files}.toml`);
    writeFileSync(path, text, { mode: 0o600 });
    return Config.load({ env: { DUDE_CONFIG: path, ...env }, defaultPath: join(dir, "absent.toml") });
  };
  const authOf = (text: string, env: Record<string, string> = {}) => loaded(text, env).auth;

  // Was: "no file keeps API keys; a valid file enables Access with auto_create by default".
  test("no file keeps API keys; a valid file enables Access with its super-admins, lower-cased", () => {
    expect(Config.load({ env: {}, defaultPath: join(dir, "absent.toml") }).auth).toEqual({ provider: "api_key" });
    expect(authOf("")).toEqual({ provider: "api_key" });
    expect(authOf("[auth]\n")).toEqual({ provider: "api_key" });
    expect(authOf(valid)).toEqual({
      provider: "cloudflare_access", public_url: "https://dude.acme.dev", super_admins: ["ops@acme.dev"],
      cloudflare_access: { team: "acme", aud: "application-audience" },
    });
    // Optional: Access with no super-admins is valid configuration (startup checks the database).
    expect(authOf(valid.replace(/super_admins.*\n/, ""))).toMatchObject({ super_admins: [] });
  });

  test("every [auth] setting has a variable that overrides the file", () => {
    expect(authOf("", {
      DUDE_AUTH_PROVIDER: "cloudflare_access", DUDE_AUTH_PUBLIC_URL: "https://env.example",
      DUDE_AUTH_SUPER_ADMINS: "a@env.example, B@env.example,",
      DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM: "envteam", DUDE_AUTH_CLOUDFLARE_ACCESS_AUD: "env-aud",
    })).toEqual({
      provider: "cloudflare_access", public_url: "https://env.example", super_admins: ["a@env.example", "b@env.example"],
      cloudflare_access: { team: "envteam", aud: "env-aud" },
    });
    expect(authOf(valid, { DUDE_AUTH_CLOUDFLARE_ACCESS_AUD: "other-aud", DUDE_AUTH_SUPER_ADMINS: "x@env.example" }))
      .toMatchObject({ super_admins: ["x@env.example"], cloudflare_access: { team: "acme", aud: "other-aud" } });
    // Every one of the five [auth] keys in the file conflicts with its variable, and each variable wins.
    const full = valid.replace(`provider = "cloudflare_access"`, `provider = "api_key"`);
    expect(authOf(full, {
      DUDE_AUTH_PROVIDER: "cloudflare_access", DUDE_AUTH_PUBLIC_URL: "https://env.example",
      DUDE_AUTH_SUPER_ADMINS: "env@env.example",
      DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM: "envteam", DUDE_AUTH_CLOUDFLARE_ACCESS_AUD: "env-aud",
    })).toEqual({
      provider: "cloudflare_access", public_url: "https://env.example", super_admins: ["env@env.example"],
      cloudflare_access: { team: "envteam", aud: "env-aud" },
    });
    // Access configured in the file is switched off by the variable alone.
    expect(authOf(valid, { DUDE_AUTH_PROVIDER: "api_key" })).toEqual({ provider: "api_key" });
  });

  test("the retired auto_create and default_organization are accepted, ignored and warned about", () => {
    const withOld = valid.replace("[auth.cloudflare_access]", `auto_create = false\ndefault_organization = "acme"\n[auth.cloudflare_access]`);
    const c = loaded(withOld, { DUDE_AUTH_AUTO_CREATE: "maybe", DUDE_AUTH_DEFAULT_ORGANIZATION: "other" });
    expect(c.auth).toEqual(authOf(valid));
    expect(c.warnings).toEqual([
      "retired: auth.auto_create; remove it; ignored: people join by invitation",
      "retired: auth.default_organization; remove it; ignored: people join by invitation",
      "retired: DUDE_AUTH_AUTO_CREATE; remove it; ignored: people join by invitation",
      "retired: DUDE_AUTH_DEFAULT_ORGANIZATION; remove it; ignored: people join by invitation",
    ]);
    // Neither reaches the resolved settings, whatever its value or type.
    expect(Object.keys(c.set()).some((k) => k.includes("AUTO_CREATE") || k.includes("DEFAULT_ORGANIZATION"))).toBe(false);
  });

  test.each([
    ["malformed TOML", `[auth]\nprovider = "api_key`, ""],
    ["duplicate key", `[auth]\nprovider = "api_key"\nprovider = "api_key"`, ""],
    ["unknown provider", `[auth]\nprovider = "google"`, "auth.provider (DUDE_AUTH_PROVIDER)"],
    ["missing public_url", valid.replace(/public_url.*\n/, ""), "auth.public_url"],
    ["missing audience", valid.replace(/aud =.*\n/, ""), "auth.cloudflare_access.aud"],
    ["team as a URL", valid.replace(`team = "acme"`, `team = "https://evil.example"`), "auth.cloudflare_access.team"],
    ["plain http origin", valid.replace("https://dude", "http://dude"), "auth.public_url"],
    ["origin with a path", valid.replace(".dev\"", ".dev/app\""), "auth.public_url"],
    // Was "wrong type" on auto_create, which is retired: the new list setting is typed instead.
    ["wrong type", valid.replace(`super_admins = ["Ops@Acme.dev"]`, `super_admins = "ops@acme.dev"`), "auth.super_admins"],
    ["a super-admin that is not an email", valid.replace("Ops@Acme.dev", "not-an-email"), "auth.super_admins (DUDE_AUTH_SUPER_ADMINS)"],
    ["unknown key", valid + "extra = 1\n", "auth.cloudflare_access.extra"],
    ["unknown table", valid + "[other]\nx = 1\n", "other"],
    ["Access settings without a provider", valid.replace(/provider.*\n/, ""), "auth.provider"],
  ])("%s fails", (_, text, names) => {
    expect(() => authOf(text)).toThrow(ConfigError);
    if (names) expect(() => authOf(text)).toThrow(names);
  });

  test("an invalid variable fails naming it", () => {
    expect(() => authOf(valid, { DUDE_AUTH_PUBLIC_URL: "http://plain.example" })).toThrow("DUDE_AUTH_PUBLIC_URL");
    // Was DUDE_AUTH_AUTO_CREATE, now retired (ignored above): the list variable is checked instead.
    expect(() => authOf(valid, { DUDE_AUTH_SUPER_ADMINS: "ops@acme.dev, nobody" })).toThrow("DUDE_AUTH_SUPER_ADMINS");
  });

  // Was: "an unreadable file and an organization that does not exist fail startup".
  test("an unreadable file, and Access with no organisations and no super-admins, fail startup", async () => {
    expect(() => Config.load({ env: { DUDE_CONFIG: "/nonexistent/dude.toml" } })).toThrow(ConfigError);
    const before = await owner`SELECT count(*)::int AS n FROM organizations`;
    // This database has organisations, so the refusal is checked against an empty one
    // holding only the table startup reads.
    const empty = `access_empty_${suffix}`;
    await owner.unsafe(`CREATE DATABASE ${empty}`);
    const url = new URL(process.env.DATABASE_URL!);
    url.pathname = `/${empty}`;
    const emptyOwner = new SQL(url.toString());
    try {
      await emptyOwner`CREATE TABLE organizations (id text PRIMARY KEY)`;
      setPool(emptyOwner);
      await expect(authFor(accessConfig({ super_admins: [] })))
        .rejects.toThrow("auth.super_admins is empty and there are no organisations: nobody could sign in");
      expect(typeof await authFor(accessConfig())).toBe("function");
    } finally {
      setPool(app);
      await emptyOwner.end();
      await owner.unsafe(`DROP DATABASE ${empty} WITH (FORCE)`);
    }
    // With organisations, an empty list is fine: their members sign in.
    expect(typeof await authFor(accessConfig({ super_admins: [] }))).toBe("function");
    expect(await owner`SELECT count(*)::int AS n FROM organizations`).toEqual(before);
  });
});

describe("the backend's startup", () => {
  const dir = mkdtempSync(join(tmpdir(), "dude-startup-auth-"));
  afterAll(() => rmSync(dir, { recursive: true, force: true }));
  const email = `startup@${suffix}.config.test`;
  // Every [auth] key in the file is wrong for this test's edge; the variables below correct them.
  const file = `
[auth]
provider = "cloudflare_access"
public_url = "https://file.example.test"
super_admins = ["file-operator@example.test"]
auto_create = true
[auth.cloudflare_access]
team = "fileteam"
aud = "file-audience"
`;
  const env = {
    DUDE_AUTH_PUBLIC_URL: ORIGIN, DUDE_AUTH_SUPER_ADMINS: `operator@${suffix}.config.test`,
    DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM: TEAM, DUDE_AUTH_CLOUDFLARE_ACCESS_AUD: AUD,
  };
  const started = (extra: Record<string, string> = {}) => {
    const path = join(dir, "dude.toml");
    writeFileSync(path, file, { mode: 0o600 });
    const edge = new Edge();
    edge.keys = [signer.jwk];
    const settings = Config.load({ env: { DUDE_CONFIG: path, ...env, ...extra }, defaultPath: join(dir, "absent.toml") });
    return backendRouter(settings, edge.fetch);
  };

  test("serves the file's [auth] with the environment over it", async () => {
    const router = await started();
    const id = await member(owner, org, email, { name: "Startup" });
    const known = await me(router, cookie(await sign(signer, { email })));
    expect(known.status).toBe(200);
    expect(known.body!.person.id).toBe(id);
    expect(known.body!.authMethod).toBe("cloudflare_access");
    // The file's audience was overridden, so a token for it is refused.
    expect((await me(router, cookie(await sign(signer, { email }, { aud: "file-audience" })))).status).toBe(401);
    // A change without the configured origin is refused; with it, allowed.
    const patch = (headers: Record<string, string>) => router.handle(new Request(`${ORIGIN}/v1/me`, {
      method: "PATCH", headers: { "content-type": "application/json", ...headers }, body: JSON.stringify({ name: "Startup" }),
    }));
    const token = await sign(signer, { email });
    expect((await patch(cookie(token))).status).toBe(403);
    expect((await patch({ ...cookie(token), origin: "https://file.example.test" })).status).toBe(403);
    expect((await patch({ ...cookie(token), origin: ORIGIN })).status).toBe(200);
    // Was: auto_create = false refuses a stranger. The file's retired auto_create = true
    // is ignored: a stranger gets gate none and nothing is written.
    const stranger = `startup-stranger@${suffix}.config.test`;
    const got = await me(router, cookie(await sign(signer, { email: stranger })));
    expect(got.status).toBe(200);
    expect(got.body!.gate).toBe("none");
    expect(await owner`SELECT id FROM accounts WHERE email = ${stranger}`).toHaveLength(0);
    expect(await owner`SELECT id FROM people WHERE email = ${stranger}`).toHaveLength(0);
    // The environment's super-admin is one; the file's is not.
    expect((await me(router, cookie(await sign(signer, { email: `operator@${suffix}.config.test` })))).body!.gate).toBe("super_admin");
    expect((await me(router, cookie(await sign(signer, { email: "file-operator@example.test" })))).body!.gate).toBe("none");
  });

  test("DUDE_AUTH_PROVIDER=api_key switches off the file's Access", async () => {
    const router = await started({ DUDE_AUTH_PROVIDER: "api_key" });
    expect((await me(router, cookie(await sign(signer, { email })))).status).toBe(401);
    const made = await createApiKey({ organizationId: org, name: "Startup key" });
    const keyed = await me(router, { authorization: `Bearer ${made.key}` });
    expect(keyed.status).toBe(200);
    expect(keyed.body!.authMethod).toBe("api_key");
  });
});
