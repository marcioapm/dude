/**
 * `dude-backend validate`, run as the binary is: a process of its own with
 * only the file for configuration. Needs no database: every address in the
 * file is a listener the test owns, and none may be connected to.
 */

import { afterAll, describe, expect, test } from "bun:test";
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resolveSettings, validate } from "../src/index.ts";

const entry = `${import.meta.dir}/../src/index.ts`;
const dir = mkdtempSync(join(tmpdir(), "dude-validate-"));
const SECRET = "S3NT1NEL-validate-db-password-5b2d";

let connections = 0;
const listener = Bun.listen({
  hostname: "127.0.0.1",
  port: 0,
  socket: { open(socket) { connections++; socket.end(); }, data() {} },
});
afterAll(() => {
  listener.stop(true);
  rmSync(dir, { recursive: true, force: true });
});
const addr = `127.0.0.1:${listener.port}`;

const good = `[database]
url = "postgres://dude_app:${SECRET}@${addr}/dude?connect_timeout=2"
[backend]
port = ${listener.port}
[orchestrator]
url = "http://${addr}"
token = "t"
[s3]
bucket = "b"
endpoint = "http://${addr}"
`;
const access = `[auth]
provider = "cloudflare_access"
public_url = "https://dude.example.com"
default_organization = "acme"
[auth.cloudflare_access]
team = "acme"
aud = "aud-tag"
`;

let files = 0;
function file(text: string, mode = 0o600): string {
  const path = join(dir, `${++files}.toml`);
  writeFileSync(path, text);
  chmodSync(path, mode);
  return path;
}

// The backend's entry point with `args`, configured by `env` alone.
async function backend(args: string[], env: Record<string, string>) {
  const proc = Bun.spawn(["bun", entry, ...args], {
    env: { PATH: process.env.PATH!, HOME: dir, ...env },
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr, exit] = await Promise.all([
    new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited,
  ]);
  expect(stdout + stderr).not.toContain(SECRET);
  return { exit, stdout, stderr };
}

// Only the file configures it: none of this machine's own settings.
const run = (path: string, ...args: string[]) => backend(["validate", ...args], { DUDE_CONFIG: path });

// Startup's own message for a refused file.
const startupError = (path: string) => resolveSettings({ env: { DUDE_CONFIG: path }, defaultPath: join(dir, "absent") }).error;

describe("dude-backend validate", () => {
  test("a valid file: exit 0 and the ok line", async () => {
    const path = file(good);
    expect(await run(path)).toEqual({ exit: 0, stdout: `ok: ${path}\n`, stderr: "" });
  });

  test("Access settings: checked, but not whether the organization exists", async () => {
    const path = file(good + access);
    expect(await run(path)).toEqual({ exit: 0, stdout: `ok: ${path}\n`, stderr: "not checked: default_organization exists\n" });
  });

  test("an unknown key: exit 1 naming it, as startup does", async () => {
    const path = file(good.replace('bucket = "b"', 'bucket = "b"\nbuckett = "x"'));
    const got = await run(path);
    expect(got.exit).toBe(1);
    expect(got.stdout).toBe("");
    expect(got.stderr).toContain("unknown key s3.buckett");
    expect(got.stderr).toBe(`${startupError(path)}\n`);
  });

  test("a missing required setting: exit 1 naming it", async () => {
    const got = await run(file(good.replace(/url = "postgres[^\n]*\n/, "")));
    expect(got).toEqual({ exit: 1, stdout: "", stderr: "database.url (DATABASE_URL) is required\n" });
  });

  test("invalid [auth]: exit 1 naming the key", async () => {
    const path = file(good + access.replace('team = "acme"', 'team = "Not A Label"'));
    const got = await run(path);
    expect(got.exit).toBe(1);
    expect(got.stderr).toContain("auth.cloudflare_access.team (DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM)");
    expect(got.stderr).toBe(`${startupError(path)}\n`);
  });

  test("a secret in a file others can read: exit 0, the warning, never the secret", async () => {
    const path = file(good, 0o644);
    const got = await run(path);
    expect(got.exit).toBe(0);
    expect(got.stdout).toBe(`ok: ${path}\n`);
    expect(got.stderr.trimEnd().split("\n")).toHaveLength(1);
    expect(got.stderr).toStartWith("warning: ");
    expect(got.stderr).toContain("database.url");
  });

  test("an extra argument: exit 2 with usage", async () => {
    expect(await run(file(good), "extra")).toEqual({ exit: 2, stdout: "", stderr: "usage: dude-backend validate\n" });
  });

  test("--version after validate: exit 2 with usage, not the version", async () => {
    expect(await run(file(good), "--version")).toEqual({ exit: 2, stdout: "", stderr: "usage: dude-backend validate\n" });
  });

  test("--version alone: the version, with no configuration at all", async () => {
    expect(await backend(["--version"], {})).toEqual({ exit: 0, stdout: "dev\n", stderr: "" });
  });

  test("no file: ok with no file", () => {
    const out: string[] = [];
    const err: string[] = [];
    const exit = validate([], { env: { DATABASE_URL: "postgres://x/y" }, defaultPath: join(dir, "absent") },
      (l) => out.push(l), (l) => err.push(l));
    expect({ exit, out, err }).toEqual({ exit: 0, out: ["ok: no file"], err: [] });
  });

  // Last, after every run above: none of them reached the listener.
  test("connected to nothing", async () => {
    await Bun.sleep(50);
    expect(connections).toBe(0);
  });
});

describe("photo storage on an old Bun", () => {
  // main's startup refuses s3.bucket on Bun < 1.4.0; validate must refuse it too.
  test("a file with s3.bucket is refused on Bun 1.3.9, naming the floor", () => {
    const { settings, error } = resolveSettings({ env: { DUDE_CONFIG: file(good) }, defaultPath: join(dir, "absent") }, "1.3.9");
    expect(settings).not.toBeNull();
    expect(error).toBe(
      "configuration: photo storage (s3.bucket) needs Bun >= 1.4.0, this is Bun 1.3.9: " +
      "earlier Bun fails every upload to a store that answers Connection: close, such as versitygw",
    );
  });

  test("the same file is accepted on Bun 1.4.0", () => {
    expect(resolveSettings({ env: { DUDE_CONFIG: file(good) }, defaultPath: join(dir, "absent") }, "1.4.0").error).toBeNull();
  });

  test("without s3.bucket an old Bun is not refused", () => {
    const noS3 = good.replace(/\[s3\]\nbucket = "b"\nendpoint = "[^"]*"\n/, "");
    expect(noS3).not.toContain("[s3]");
    expect(resolveSettings({ env: { DUDE_CONFIG: file(noS3) } }, "1.3.9").error).toBeNull();
  });
});
