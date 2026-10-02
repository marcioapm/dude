/**
 * The backend's settings: one TOML file shared with the orchestrator, and
 * the environment.
 *
 * The file is `DUDE_CONFIG` if set, else /etc/dude/dude.toml if it exists,
 * else none. Every setting also has a variable, and a non-empty variable
 * overrides the file. `KEYS` is the schema, the same as the orchestrator's
 * (orchestrator/internal/config): a key either process knows is accepted
 * here, and only the keys the backend uses are type-checked and read.
 * Resolved once before the server listens: an unreadable `DUDE_CONFIG`, an
 * unknown key or an invalid value stops the process.
 */

import { readFileSync, statSync } from "node:fs";
import { z } from "zod";
import { withoutTenant } from "./db/client.ts";

export const DEFAULT_PATH = "/etc/dude/dude.toml";

type Kind = "string" | "int" | "float" | "bool" | "list" | "duration";
type Use = "backend" | "orchestrator" | "both";

export interface Key {
  /** The file key, dotted: "auth.cloudflare_access.team". */
  readonly name: string;
  readonly env: string;
  readonly kind: Kind;
  readonly use: Use;
  /** As the variable's text; "" for none. */
  readonly default: string;
  readonly secret: boolean;
}

const key = (name: string, env: string, kind: Kind, use: Use, dflt = "", secret = false): Key =>
  ({ name, env, kind, use, default: dflt, secret });

// In the file's order; tests/fixtures/config/keys.json must match it, and
// the Go schema is checked against the same file.
export const KEYS: readonly Key[] = [
  key("database.url", "DATABASE_URL", "string", "both", "", true),
  key("backend.port", "PORT", "int", "backend", "3000"),
  key("backend.web_dir", "DUDE_WEB_DIR", "string", "backend"),
  key("orchestrator.url", "DUDE_ORCHESTRATOR_URL", "string", "backend"),
  key("orchestrator.token", "DUDE_ORCHESTRATOR_TOKEN", "string", "both", "", true),
  key("orchestrator.listen", "DUDE_ORCHESTRATOR_LISTEN", "string", "orchestrator", "127.0.0.1:3100"),
  key("orchestrator.pr_reconcile", "DUDE_PR_RECONCILE", "duration", "orchestrator", "15m"),
  key("orchestrator.park_after", "DUDE_PARK_AFTER", "duration", "orchestrator", "0s"),
  key("orchestrator.idle_after", "DUDE_IDLE_AFTER", "duration", "orchestrator", "0s"),
  key("orchestrator.diff_every", "DUDE_DIFF_EVERY", "duration", "orchestrator", "15s"),
  key("orchestrator.machine_usd_per_hour", "DUDE_MACHINE_USD_PER_HOUR", "float", "orchestrator", "0.20"),
  key("orchestrator.lux_cost_every", "DUDE_LUX_COST_EVERY", "duration", "orchestrator", "2m"),
  key("orchestrator.keep_stopped", "DUDE_KEEP_STOPPED", "duration", "orchestrator", "168h"),
  key("s3.bucket", "DUDE_S3_BUCKET", "string", "backend"),
  key("s3.endpoint", "DUDE_S3_ENDPOINT", "string", "backend"),
  key("s3.region", "DUDE_S3_REGION", "string", "backend", "us-east-1"),
  key("s3.access_key", "DUDE_S3_ACCESS_KEY", "string", "backend"),
  key("s3.secret_key", "DUDE_S3_SECRET_KEY", "string", "backend", "", true),
  key("lux.url", "LUX_URL", "string", "orchestrator"),
  key("lux.console_url", "LUX_CONSOLE_URL", "string", "orchestrator"),
  key("lux.api_key", "LUX_API_KEY", "string", "orchestrator", "", true),
  key("previews.domain", "DUDE_PREVIEW_DOMAIN", "string", "orchestrator"),
  key("previews.reap_after", "DUDE_PREVIEW_REAP_AFTER", "duration", "orchestrator", "168h"),
  key("llm.url", "DUDE_LLM_URL", "string", "orchestrator"),
  key("llm.key", "DUDE_LLM_KEY", "string", "orchestrator", "", true),
  key("embeddings.url", "DUDE_EMBEDDINGS_URL", "string", "orchestrator"),
  key("embeddings.key", "DUDE_EMBEDDINGS_KEY", "string", "orchestrator", "", true),
  key("embeddings.model", "DUDE_EMBEDDINGS_MODEL", "string", "orchestrator", "gemini-embedding-2"),
  key("embeddings.dimensions", "DUDE_EMBEDDINGS_DIMENSIONS", "int", "orchestrator", "768"),
  key("agent.image", "DUDE_AGENT_IMAGE", "string", "orchestrator", "localhost/dude-runtime:dev"),
  key("agent.timeout", "DUDE_AGENT_TIMEOUT", "string", "orchestrator"),
  key("agent.egress", "DUDE_AGENT_EGRESS", "list", "orchestrator"),
  key("agent.nested_containers", "DUDE_AGENT_NESTED_CONTAINERS", "bool", "orchestrator", "false"),
  key("registry.auth", "DUDE_REGISTRY_AUTH", "string", "orchestrator", "none"),
  key("registry.host", "DUDE_REGISTRY", "string", "orchestrator"),
  key("registry.credential", "DUDE_REGISTRY_CREDENTIAL", "string", "orchestrator", "", true),
  key("registry.ecr_role_arn", "DUDE_ECR_ROLE_ARN", "string", "orchestrator"),
  key("tools.listen", "DUDE_TOOLS_LISTEN", "string", "orchestrator"),
  key("tools.url", "DUDE_TOOLS_URL", "string", "orchestrator"),
  key("tools.service", "DUDE_TOOLS_SERVICE", "bool", "orchestrator", "true"),
  key("tools.key", "DUDE_TOOLS_KEY", "string", "orchestrator", "", true),
  key("vapid.public_key", "DUDE_VAPID_PUBLIC_KEY", "string", "orchestrator"),
  key("vapid.private_key", "DUDE_VAPID_PRIVATE_KEY", "string", "orchestrator", "", true),
  key("vapid.subject", "DUDE_VAPID_SUBJECT", "string", "orchestrator", "mailto:dude@localhost"),
  key("factory.logins", "DUDE_FACTORY_LOGINS", "list", "orchestrator"),
  key("auth.provider", "DUDE_AUTH_PROVIDER", "string", "backend", "api_key"),
  key("auth.public_url", "DUDE_AUTH_PUBLIC_URL", "string", "backend"),
  key("auth.auto_create", "DUDE_AUTH_AUTO_CREATE", "bool", "backend", "true"),
  key("auth.default_organization", "DUDE_AUTH_DEFAULT_ORGANIZATION", "string", "backend"),
  key("auth.cloudflare_access.team", "DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM", "string", "backend"),
  key("auth.cloudflare_access.aud", "DUDE_AUTH_CLOUDFLARE_ACCESS_AUD", "string", "backend"),
];

const byEnv = new Map(KEYS.map((k) => [k.env, k]));
const byName = new Map(KEYS.map((k) => [k.name, k]));
const usedHere = (k: Key) => k.use !== "orchestrator";
export const label = (k: Key) => `${k.name} (${k.env})`;

/**
 * A setting a release removed. For one more release both processes accept
 * it, from the file or the environment, ignore it and warn, so the previous
 * release's configuration still starts the new one.
 */
export interface RetiredKey {
  readonly name: string;
  readonly env: string;
}

// tests/fixtures/config/keys.json's "retired" must match it, as the Go list must.
export const RETIRED: readonly RetiredKey[] = [];

/** The warning for a retired setting: its key or variable, never its value. */
const retiredWarning = (name: string) => `retired: ${name}; remove it`;

type Value = string | number | boolean | string[];

export class ConfigError extends Error {
  override name = "ConfigError";
}

// Go's time.ParseDuration grammar: "0", or signed decimal-and-unit terms.
const DURATION = /^[+-]?(?:0|(?:(?:\d+\.?\d*|\.\d+)(?:ns|us|µs|μs|ms|s|m|h))+)$/;
const FLOAT = /^[+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$/;

const article: Record<Kind, string> = {
  string: "a string", int: "an integer", float: "a number", bool: "a boolean", list: "an array of strings",
  duration: "a duration string",
};

function tomlType(v: unknown): string {
  if (Array.isArray(v)) return "an array";
  if (v !== null && typeof v === "object") return "a table";
  if (typeof v === "number") return Number.isInteger(v) ? "an integer" : "a float";
  return typeof v === "boolean" ? "a boolean" : `a ${typeof v}`;
}

/** A value from the file, checked against its key's kind. */
function fromFile(k: Key, v: unknown): Value {
  switch (k.kind) {
    case "string":
      if (typeof v === "string") return v;
      break;
    case "duration":
      if (typeof v === "string") {
        if (!DURATION.test(v)) throw new ConfigError(`not a duration: ${JSON.stringify(v)}`);
        return v;
      }
      break;
    case "int":
      if (typeof v === "number" && Number.isInteger(v)) return v;
      break;
    case "float":
      if (typeof v === "number" && Number.isFinite(v)) return v;
      break;
    case "bool":
      if (typeof v === "boolean") return v;
      break;
    case "list":
      if (Array.isArray(v) && v.every((item) => typeof item === "string")) return v as string[];
      throw new ConfigError("want an array of strings");
  }
  throw new ConfigError(`want ${article[k.kind]}, not ${tomlType(v)}`);
}

/** A variable's text parsed as its key's kind; lists are comma-separated. */
function fromEnv(k: Key, raw: string): Value {
  switch (k.kind) {
    case "duration":
      if (!DURATION.test(raw)) throw new ConfigError(`not a duration: ${JSON.stringify(raw)}`);
      return raw;
    case "int":
      if (!/^[+-]?\d+$/.test(raw)) throw new ConfigError(`not an integer: ${JSON.stringify(raw)}`);
      return Number(raw);
    case "float":
      if (!FLOAT.test(raw)) throw new ConfigError(`not a number: ${JSON.stringify(raw)}`);
      return Number(raw);
    case "bool":
      switch (raw.toLowerCase()) {
        case "true": case "on": case "1": return true;
        case "false": case "off": case "0": return false;
      }
      throw new ConfigError(`want true, false, on, off, 1 or 0, not ${JSON.stringify(raw)}`);
    case "list":
      return raw.split(",").map((s) => s.trim()).filter((s) => s !== "");
    default:
      return raw;
  }
}

// The schema's shape: each table's exact child segments, by the table's
// segment path ("" for the root, "auth\0cloudflare_access"); true marks a
// leaf. Paths are joined with NUL so a quoted segment holding a dot
// ("database.url" = …) cannot pass for two segments.
const SEP = "\0";
const shape = new Map<string, Map<string, boolean>>([["", new Map()]]);
for (const k of KEYS) {
  const parts = k.name.split(".");
  parts.forEach((part, i) => {
    const parent = parts.slice(0, i).join(SEP);
    const leaf = i === parts.length - 1;
    shape.get(parent)!.set(part, leaf);
    const path = parts.slice(0, i + 1).join(SEP);
    if (!leaf && !shape.has(path)) shape.set(path, new Map());
  });
}

/**
 * Every leaf of the parsed file, by dotted name, and the retired keys it
 * holds; unknown keys and misplaced tables are refused.
 */
function leaves(doc: Record<string, unknown>, retired: readonly RetiredKey[]): { found: Map<string, unknown>; gone: string[] } {
  const out = new Map<string, unknown>();
  const unknown: string[] = [];
  const gone: string[] = [];
  const retiredNames = new Set(retired.map((r) => r.name));
  const retiredTables = new Set(retired.flatMap((r) => {
    const parts = r.name.split(".");
    return parts.slice(1).map((_, i) => parts.slice(0, i + 1).join("."));
  }));
  const walk = (obj: Record<string, unknown>, path: string[]) => {
    const children = shape.get(path.join(SEP));
    for (const [k, v] of Object.entries(obj)) {
      const segments = [...path, k];
      const name = segments.map((s) => /^[A-Za-z0-9_-]+$/.test(s) ? s : JSON.stringify(s)).join(".");
      const leaf = children?.get(k);
      const table = v !== null && typeof v === "object" && !Array.isArray(v);
      if (leaf === false) {
        if (!table) throw new ConfigError(`${name}: want a table, not ${tomlType(v)}`);
        walk(v as Record<string, unknown>, segments);
      } else if (leaf === true) {
        out.set(name, v);
      } else if (retiredNames.has(name)) {
        gone.push(name);
      } else if (table && retiredTables.has(name)) {
        // No schema table here (children is undefined): only retired keys pass.
        walk(v as Record<string, unknown>, segments);
      } else {
        unknown.push(name);
      }
    }
  };
  walk(doc, []);
  if (unknown.length) throw new ConfigError(`unknown key ${unknown.join(", ")}`);
  return { found: out, gone: gone.sort() };
}

function readFile(env: Env, defaultPath: string): { path: string; text: string } | null {
  const named = env.DUDE_CONFIG;
  if (named) {
    try {
      return { path: named, text: readFileSync(named, "utf8") };
    } catch (err) {
      throw new ConfigError(`DUDE_CONFIG: ${(err as Error).message}`);
    }
  }
  try {
    return { path: defaultPath, text: readFileSync(defaultPath, "utf8") };
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "ENOENT") return null;
    throw new ConfigError(`${defaultPath}: ${(err as Error).message}`);
  }
}

/** Set when a file others on the host can read holds a secret. */
function secretWarning(path: string, fromFile: string[]): string | null {
  let mode: number;
  try {
    mode = statSync(path).mode & 0o777;
  } catch {
    return null;
  }
  const secrets = KEYS.filter((k) => k.secret && fromFile.includes(k.env)).map((k) => k.name);
  if ((mode & 0o044) === 0 || secrets.length === 0) return null;
  return `${path} is readable by group or others (mode ${mode.toString(8).padStart(4, "0")}) and holds secrets: ` +
    `${secrets.join(", ")}; chmod it 0600 or supply them by environment`;
}

// A Cloudflare Zero Trust team name: one DNS label, so the only origin the
// verifier ever fetches from is https://<team>.cloudflareaccess.com.
const teamLabel = z.string().regex(/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/, "a lowercase DNS label");

const publicUrl = z.string().transform((raw, ctx) => {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: "an absolute URL" });
    return z.NEVER;
  }
  if (url.protocol !== "https:" || url.username || url.password || url.search || url.hash || url.pathname !== "/") {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: "an https origin with no path, query or credentials" });
    return z.NEVER;
  }
  return url.origin;
});

const accessAuth = z.object({
  provider: z.literal("cloudflare_access"),
  public_url: publicUrl,
  auto_create: z.boolean(),
  default_organization: z.string().min(1),
  cloudflare_access: z.object({ team: teamLabel, aud: z.string().min(1) }),
});

export type AccessConfig = z.infer<typeof accessAuth>;
export type AuthConfig = { provider: "api_key" } | AccessConfig;

type Env = Record<string, string | undefined>;

export interface LoadOptions {
  /** The environment; process.env when absent. */
  env?: Env;
  /** Replaces DEFAULT_PATH, for tests. */
  defaultPath?: string;
  /** Replaces RETIRED, for tests. */
  retired?: readonly RetiredKey[];
}

/** The backend's resolved settings. */
export class Config {
  private constructor(
    /** The file read; null for none. */
    readonly path: string | null,
    private readonly values: Map<string, Value>,
    private readonly sources: Map<string, "file" | "env">,
    /** Problems that do not stop startup, for the caller to log. */
    readonly warnings: string[],
  ) {
    const port = this.port;
    if (port < 0 || port > 65535) throw new ConfigError(`${label(byEnv.get("PORT")!)}: not a port: ${port}`);
    this.auth = this.resolveAuth();
  }

  readonly auth: AuthConfig;

  static load(opts: LoadOptions = {}): Config {
    const env = opts.env ?? process.env;
    const retired = opts.retired ?? RETIRED;
    const values = new Map<string, Value>();
    const sources = new Map<string, "file" | "env">();
    const warnings: string[] = [];
    const file = readFile(env, opts.defaultPath ?? DEFAULT_PATH);
    if (file) {
      const where = (err: unknown) => new ConfigError(`${file.path}: ${(err as Error).message}`);
      let doc: Record<string, unknown>;
      try {
        doc = Bun.TOML.parse(file.text) as Record<string, unknown>;
      } catch (err) {
        throw new ConfigError(`${file.path}: not valid TOML: ${(err as Error).message}`);
      }
      let found: Map<string, unknown>;
      try {
        const read = leaves(doc, retired);
        found = read.found;
        warnings.push(...read.gone.map(retiredWarning));
      } catch (err) {
        throw where(err);
      }
      for (const [name, raw] of found) {
        const k = byName.get(name)!;
        // An empty string is unset, as an empty variable is: the default
        // applies, and a required key is still missing.
        if (raw === "" && (k.kind === "string" || k.kind === "duration")) continue;
        let v: Value;
        try {
          v = fromFile(k, raw);
        } catch (err) {
          if (!usedHere(k)) continue; // the orchestrator's to refuse
          throw new ConfigError(`${file.path}: ${label(k)}: ${(err as Error).message}`);
        }
        values.set(k.env, v);
        sources.set(k.env, "file");
      }
      const warning = secretWarning(file.path, [...sources.keys()]);
      if (warning) warnings.push(warning);
    }
    for (const r of retired) {
      if (r.env && env[r.env]) warnings.push(retiredWarning(r.env));
    }
    for (const k of KEYS) {
      const raw = env[k.env];
      if (!usedHere(k) || !raw) continue;
      try {
        values.set(k.env, fromEnv(k, raw));
      } catch (err) {
        throw new ConfigError(`${k.env}: ${(err as Error).message}`);
      }
      sources.set(k.env, "env");
    }
    return new Config(file?.path ?? null, values, sources, warnings);
  }

  private key(env: string, kind: Kind): Key {
    const k = byEnv.get(env);
    if (!k || !usedHere(k)) throw new Error(`config: ${env} is not a backend setting`);
    if (k.kind !== kind) throw new Error(`config: ${env} is ${k.kind}, not ${kind}`);
    return k;
  }

  private value(env: string, kind: Kind): Value | undefined {
    const k = this.key(env, kind);
    return this.values.get(env) ?? (k.default === "" ? undefined : fromEnv(k, k.default));
  }

  /** A string setting or its default; undefined with neither. */
  string(env: string): string | undefined {
    return this.value(env, "string") as string | undefined;
  }

  int(env: string): number {
    return (this.value(env, "int") as number | undefined) ?? 0;
  }

  bool(env: string): boolean {
    return (this.value(env, "bool") as boolean | undefined) ?? false;
  }

  /** Where a setting came from: "file", "env", or undefined for its default. */
  from(env: string): "file" | "env" | undefined {
    return this.sources.get(env);
  }

  /**
   * Every setting the file or the environment gave, by variable, including
   * the orchestrator's file keys of the right type: for tests comparing the
   * two processes' reading of one file.
   */
  set(): Record<string, Value> {
    return Object.fromEntries(this.values);
  }

  get databaseUrl() { return this.string("DATABASE_URL"); }
  get webDir() { return this.string("DUDE_WEB_DIR"); }
  get port() { return this.int("PORT"); }

  private resolveAuth(): AuthConfig {
    const provider = this.string("DUDE_AUTH_PROVIDER");
    const accessKeys = KEYS.filter((k) => k.name.startsWith("auth.") && k.env !== "DUDE_AUTH_PROVIDER" && this.from(k.env));
    if (provider === "api_key") {
      // Settings for Access without a provider would silently sign no one in with it.
      if (!this.from("DUDE_AUTH_PROVIDER") && accessKeys.length) {
        throw new ConfigError(`${accessKeys.map(label).join(", ")} set, but ${label(byEnv.get("DUDE_AUTH_PROVIDER")!)} is not: ` +
          "set it to cloudflare_access, or api_key to ignore them");
      }
      return { provider: "api_key" };
    }
    if (provider !== "cloudflare_access") {
      throw new ConfigError(`${label(byEnv.get("DUDE_AUTH_PROVIDER")!)}: want api_key or cloudflare_access, not ${JSON.stringify(provider)}`);
    }
    const parsed = accessAuth.safeParse({
      provider,
      public_url: this.string("DUDE_AUTH_PUBLIC_URL"),
      auto_create: this.bool("DUDE_AUTH_AUTO_CREATE"),
      default_organization: this.string("DUDE_AUTH_DEFAULT_ORGANIZATION"),
      cloudflare_access: {
        team: this.string("DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM"),
        aud: this.string("DUDE_AUTH_CLOUDFLARE_ACCESS_AUD"),
      },
    });
    if (!parsed.success) {
      const issues = parsed.error.issues.map((i) => {
        const k = byName.get(`auth.${i.path.join(".")}`);
        const message = i.code === z.ZodIssueCode.invalid_type && i.received === "undefined" ? "required with cloudflare_access" : i.message;
        return `${k ? label(k) : `auth.${i.path.join(".")}`}: ${message}`;
      });
      throw new ConfigError(issues.join("; "));
    }
    return parsed.data;
  }
}

let current: Config | null = null;

/**
 * The backend's settings: the ones `useConfig` installed at startup, else
 * resolved from process.env and the default file on first use.
 */
export function config(): Config {
  return current ??= Config.load();
}

/** Install `next` as the settings every read site sees; null resolves them again on next use. */
export function useConfig(next: Config | null): void {
  current = next;
}

/**
 * The id of the organization whose slug is `slug`. Tenant-blind because the
 * organization is what is being found; everything after uses `withOrg`.
 * Never creates one.
 */
export async function organizationBySlug(slug: string): Promise<string> {
  const rows = await withoutTenant(async ({ sql }) =>
    (await sql`SELECT id FROM organizations WHERE slug = ${slug}`) as Array<{ id: string }>);
  if (!rows[0]) throw new ConfigError(`auth.default_organization: no organization has slug "${slug}"`);
  return rows[0].id;
}
