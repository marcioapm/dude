/**
 * The backend's optional configuration file, named by `DUDE_CONFIG`.
 *
 * Read and validated once before the server listens: a file that is
 * unreadable, malformed or names something that does not exist stops the
 * process instead of serving with a guess. Without the variable nothing
 * changes — API keys are the only credential, as before the file existed.
 * Database and service settings stay in the environment.
 */

import { z } from "zod";
import { withoutTenant } from "./db/client.ts";

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

const apiKeyAuth = z.object({ provider: z.literal("api_key") }).strict();

const accessAuth = z
  .object({
    provider: z.literal("cloudflare_access"),
    public_url: publicUrl,
    auto_create: z.boolean().default(true),
    default_organization: z.string().min(1),
    cloudflare_access: z.object({ team: teamLabel, aud: z.string().min(1) }).strict(),
  })
  .strict();

const fileSchema = z
  .object({
    auth: z.discriminatedUnion("provider", [apiKeyAuth, accessAuth]).default({ provider: "api_key" }),
  })
  .strict();

export type AuthConfig = z.infer<typeof fileSchema>["auth"];
export type AccessConfig = Extract<AuthConfig, { provider: "cloudflare_access" }>;

export class ConfigError extends Error {
  override name = "ConfigError";
}

/** Parse and validate a configuration file's text. `auth.provider` absent means API keys. */
export function parseConfig(text: string): { auth: AuthConfig } {
  let raw: unknown;
  try {
    raw = Bun.TOML.parse(text);
  } catch (err) {
    throw new ConfigError(`not valid TOML: ${(err as Error).message}`);
  }
  // A bare [auth] table with no provider is the default, API keys.
  if (raw && typeof raw === "object" && "auth" in raw) {
    const auth = (raw as { auth: unknown }).auth;
    if (auth && typeof auth === "object" && !("provider" in auth)) (auth as Record<string, unknown>).provider = "api_key";
  }
  const parsed = fileSchema.safeParse(raw);
  if (!parsed.success) {
    const issues = parsed.error.issues.map((i) => `${i.path.join(".") || "(file)"}: ${i.message}`);
    throw new ConfigError(`invalid configuration: ${issues.join("; ")}`);
  }
  return parsed.data;
}

export async function readConfig(path: string | undefined): Promise<{ auth: AuthConfig }> {
  if (!path) return { auth: { provider: "api_key" } };
  let text: string;
  try {
    text = await Bun.file(path).text();
  } catch (err) {
    throw new ConfigError(`cannot read DUDE_CONFIG ${path}: ${(err as Error).message}`);
  }
  return parseConfig(text);
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
