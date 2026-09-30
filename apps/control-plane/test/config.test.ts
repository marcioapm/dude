/**
 * The backend's settings loader: the same file and key table as the
 * orchestrator's (orchestrator/internal/config), resolved the same way.
 * Needs no database.
 */

import { afterAll, describe, expect, test } from "bun:test";
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Config, ConfigError, KEYS } from "../src/config.ts";

const fixtures = `${import.meta.dir}/../../../tests/fixtures/config`;
const dir = mkdtempSync(join(tmpdir(), "dude-config-"));
afterAll(() => rmSync(dir, { recursive: true, force: true }));
const absent = join(dir, "absent.toml");

let files = 0;
function file(text: string, mode = 0o600): string {
  const path = join(dir, `${++files}.toml`);
  writeFileSync(path, text);
  chmodSync(path, mode);
  return path;
}

const load = (env: Record<string, string>) => Config.load({ env, defaultPath: absent });

describe("resolving", () => {
  test("without a file the environment alone configures, as before the file", () => {
    const c = load({ PORT: "4000", DUDE_S3_BUCKET: "b", DATABASE_URL: "postgres://x" });
    expect(c.path).toBeNull();
    expect(c.port).toBe(4000);
    expect(c.string("DUDE_S3_BUCKET")).toBe("b");
    expect(c.databaseUrl).toBe("postgres://x");
    // Unset, a setting is its default, or undefined.
    expect(c.string("DUDE_S3_REGION")).toBe("us-east-1");
    expect(c.webDir).toBeUndefined();
    expect(c.auth).toEqual({ provider: "api_key" });
    expect(c.from("PORT")).toBe("env");
    expect(c.from("DUDE_S3_REGION")).toBeUndefined();
  });

  test("a file alone configures", () => {
    const path = file(`[backend]\nport = 4001\nweb_dir = "/web"\n[s3]\nbucket = "b"\n[orchestrator]\nurl = "http://o"\n`);
    const c = load({ DUDE_CONFIG: path });
    expect(c.path).toBe(path);
    expect([c.port, c.webDir, c.string("DUDE_S3_BUCKET"), c.string("DUDE_ORCHESTRATOR_URL")])
      .toEqual([4001, "/web", "b", "http://o"]);
    expect(c.from("PORT")).toBe("file");
  });

  test("the environment overrides the file key by key: a string, a number, a boolean, a list", () => {
    const path = file(`
[backend]
port = 4001
web_dir = "/file"
[s3]
region = "eu-west-1"
[auth]
provider = "api_key"
auto_create = true
[tools]
service = true
[agent]
egress = ["file.example"]
`);
    const c = load({
      DUDE_CONFIG: path, DUDE_WEB_DIR: "/env", PORT: "4002", DUDE_AUTH_AUTO_CREATE: "false",
      DUDE_TOOLS_SERVICE: "off", DUDE_AGENT_EGRESS: "a.example, b.example,",
    });
    expect([c.webDir, c.port, c.bool("DUDE_AUTH_AUTO_CREATE")]).toEqual(["/env", 4002, false]);
    // The orchestrator's variables are parsed only by it; its file keys are still read and typed.
    expect(c.set().DUDE_TOOLS_SERVICE).toBe(true);
    expect(c.set().DUDE_AGENT_EGRESS).toEqual(["file.example"]);
    // Keys only the file sets keep the file's value; an empty variable is unset.
    expect(c.string("DUDE_S3_REGION")).toBe("eu-west-1");
    expect(load({ DUDE_CONFIG: path, DUDE_WEB_DIR: "" }).webDir).toBe("/file");
  });

  test("an empty string in the file is unset: the default applies, a required key is missing", () => {
    const path = file(`[s3]\nregion = ""\nbucket = ""\n[backend]\nweb_dir = ""\n[auth]\nprovider = ""\n` +
      `[orchestrator]\nlisten = ""\nurl = ""\npr_reconcile = ""\n[database]\nurl = ""\n`);
    const c = load({ DUDE_CONFIG: path });
    expect(c.string("DUDE_S3_REGION")).toBe("us-east-1");
    expect(c.from("DUDE_S3_REGION")).toBeUndefined();
    expect(c.string("DUDE_S3_BUCKET")).toBeUndefined();
    expect(c.webDir).toBeUndefined();
    expect(c.databaseUrl).toBeUndefined();
    expect(c.string("DUDE_ORCHESTRATOR_URL")).toBeUndefined();
    expect(c.auth).toEqual({ provider: "api_key" });
    // Nor are the orchestrator's empty keys recorded, as the Go loader records none.
    expect(c.set()).toEqual({});
    expect(load({ DUDE_CONFIG: path, DUDE_S3_REGION: "eu-west-2" }).string("DUDE_S3_REGION")).toBe("eu-west-2");
  });

  test("DUDE_CONFIG naming a missing file is refused", () => {
    expect(() => load({ DUDE_CONFIG: join(dir, "missing.toml") })).toThrow(/DUDE_CONFIG/);
  });

  test("the default path is read when present, and DUDE_CONFIG wins over it", () => {
    const fallback = file(`[backend]\nweb_dir = "/default"\n`);
    expect(Config.load({ env: {}, defaultPath: fallback }).webDir).toBe("/default");
    const named = file(`[backend]\nweb_dir = "/named"\n`);
    expect(Config.load({ env: { DUDE_CONFIG: named }, defaultPath: fallback }).webDir).toBe("/named");
  });
});

describe("strictness", () => {
  test.each([
    ["[lux]\nurl = \"x\"\ntoken = \"y\"\n", "lux.token"],
    ["[nonsense]\nx = 1\n", "nonsense"],
    ["top = 1\n", "top"],
    ["[auth.cloudflare_access]\nteam_name = \"x\"\n", "auth.cloudflare_access.team_name"],
    ["[Orchestrator]\nlisten = \"127.0.0.1:1\"\n", "Orchestrator"],
    ["[orchestrator]\nListen = \"127.0.0.1:1\"\n", "orchestrator.Listen"],
    ["[orchestrator]\nlisten = \"127.0.0.1:1\"\nListen = \"0.0.0.0:1\"\n", "orchestrator.Listen"],
    ["\"database.url\" = \"x\"\n", "\"database.url\""],
    ["[\"orchestrator.listen\"]\nx = 1\n", "\"orchestrator.listen\""],
  ])("an unknown key is refused by name: %j", (text, name) => {
    expect(() => load({ DUDE_CONFIG: file(text) })).toThrow(ConfigError);
    expect(() => load({ DUDE_CONFIG: file(text) })).toThrow(`unknown key ${name}`);
  });

  test.each([
    ["[backend]\nport = \"3000\"\n", "backend.port (PORT)"],
    ["[backend]\nport = 3000.5\n", "backend.port (PORT)"],
    ["[backend]\nweb_dir = 1\n", "backend.web_dir (DUDE_WEB_DIR)"],
    ["[auth]\nauto_create = \"yes\"\n", "auth.auto_create (DUDE_AUTH_AUTO_CREATE)"],
    ["[s3]\nbucket = [\"b\"]\n", "s3.bucket (DUDE_S3_BUCKET)"],
    ["backend = 1\n", "backend"],
  ])("a wrong type is refused by name: %j", (text, name) => {
    expect(() => load({ DUDE_CONFIG: file(text) })).toThrow(name);
  });

  test("an invalid variable is refused by name", () => {
    expect(() => load({ PORT: "http" })).toThrow("PORT");
    expect(() => load({ PORT: "70000" })).toThrow("PORT");
    expect(() => load({ DUDE_AUTH_AUTO_CREATE: "maybe" })).toThrow("DUDE_AUTH_AUTO_CREATE");
  });

  test("a non-finite rate is never resolved; it is the orchestrator's to refuse", () => {
    // Bun 1.3's TOML parser yields "nan"/"inf" as strings and folds +inf/-inf/±nan to ±0,
    // so only the bare spellings are distinguishable here; the backend reads no float key.
    for (const v of ["nan", "inf"]) {
      const c = load({ DUDE_CONFIG: file(`[orchestrator]\nmachine_usd_per_hour = ${v}\n`) });
      expect(c.set()).not.toHaveProperty("DUDE_MACHINE_USD_PER_HOUR");
    }
    expect(load({ DUDE_CONFIG: file(`[orchestrator]\nmachine_usd_per_hour = 2\n`) }).set().DUDE_MACHINE_USD_PER_HOUR).toBe(2);
    expect(load({ DUDE_CONFIG: file(`[orchestrator]\nmachine_usd_per_hour = 0.35\n`) }).set().DUDE_MACHINE_USD_PER_HOUR).toBe(0.35);
  });

  test("the orchestrator's sections are accepted; its values are its own to refuse", () => {
    const path = file(`
[lux]
url = "https://lux.example"
[embeddings]
dimensions = 768
[orchestrator]
listen = "127.0.0.1:3100"
pr_reconcile = "5m"
[factory]
logins = ["bot"]
`);
    expect(load({ DUDE_CONFIG: path }).set().LUX_URL).toBe("https://lux.example");
    expect(() => load({ DUDE_CONFIG: file(`[embeddings]\ndimensions = "768"\n[orchestrator]\npr_reconcile = 15\n`) }))
      .not.toThrow();
    // Nor does one of its variables reach the backend, or fail it.
    expect(() => load({ DUDE_EMBEDDINGS_DIMENSIONS: "seven", DUDE_TOOLS_SERVICE: "maybe" })).not.toThrow();
  });
});

describe("secrets in the file", () => {
  const secret = `[orchestrator]\ntoken = "service-token"\n`;
  test("a file others can read that holds a secret warns, naming the key and not the value", () => {
    for (const mode of [0o644, 0o640]) {
      const c = load({ DUDE_CONFIG: file(secret, mode) });
      expect(c.warnings).toHaveLength(1);
      expect(c.warnings[0]).toContain("orchestrator.token");
      expect(c.warnings[0]).not.toContain("service-token");
    }
    // Either process warns about either's secrets: the file is shared.
    expect(load({ DUDE_CONFIG: file(`[lux]\napi_key = "k"\n`, 0o644) }).warnings[0]).toContain("lux.api_key");
  });

  test("the warning stands when the environment overrides the file's secret", () => {
    const c = load({ DUDE_CONFIG: file(secret, 0o644), DUDE_ORCHESTRATOR_TOKEN: "env-token" });
    expect(c.string("DUDE_ORCHESTRATOR_TOKEN")).toBe("env-token");
    expect(c.warnings).toHaveLength(1);
    expect(c.warnings[0]).toContain("orchestrator.token");
    expect(c.warnings[0]).not.toContain("service-token");
  });

  test("a private file, a file without secrets, or a secret by environment does not warn", () => {
    expect(load({ DUDE_CONFIG: file(secret, 0o600) }).warnings).toEqual([]);
    expect(load({ DUDE_CONFIG: file(`[backend]\nport = 3000\n`, 0o644) }).warnings).toEqual([]);
    expect(load({ DUDE_CONFIG: file("", 0o644), DUDE_ORCHESTRATOR_TOKEN: "t" }).warnings).toEqual([]);
  });
});

describe("the shared fixture", () => {
  test("the schema is tests/fixtures/config/keys.json, as the Go schema is", async () => {
    const want = await Bun.file(`${fixtures}/keys.json`).json();
    expect(KEYS.map((k) => ({ ...k }))).toEqual(want);
  });

  test("full.toml resolves to full.json, as the Go suite requires", async () => {
    const want = await Bun.file(`${fixtures}/full.json`).json();
    expect(Object.keys(want).sort()).toEqual(KEYS.map((k) => k.env).sort());
    expect(load({ DUDE_CONFIG: `${fixtures}/full.toml` }).set()).toEqual(want);
  });

  test("the documented example loads", () => {
    expect(() => load({ DUDE_CONFIG: `${import.meta.dir}/../../../docs/dude.example.toml` })).not.toThrow();
  });

  // invalid/*.toml are refused by the Go suite too; each file's first line
  // is "# <the error it must contain>".
  const invalid = [...new Bun.Glob("*.toml").scanSync(`${fixtures}/invalid`)].sort();
  test("there are shared invalid files", () => expect(invalid.length).toBeGreaterThan(0));
  test.each(invalid)("invalid/%s is refused", (name) => {
    const path = `${fixtures}/invalid/${name}`;
    const want = readFileSync(path, "utf8").split("\n")[0]!.replace(/^# /, "");
    expect(() => load({ DUDE_CONFIG: path })).toThrow(want);
  });
});
