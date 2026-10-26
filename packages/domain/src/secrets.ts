import { z } from "zod";
import { ENV_NAME } from "./servers.ts";

/** Project environment secrets are write-only; clients receive names, recipients and hints. */

/**
 * Names dude declares as lux secrets of its own (orchestrator/internal/phases):
 * a preview declaring one twice is refused by lux.
 */
export const DUDE_SECRET_NAMES = [
  "GIT_TOKEN", "DUDE_TOOLS_AUTH", "DUDE_REGISTRY_AUTH", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL",
  "ANTHROPIC_CUSTOM_HEADERS", "OPENAI_API_KEY", "CODEX_CONFIG", "DISABLE_AUTOUPDATER",
  "TERM", "FORCE_COLOR", "CLICOLOR_FORCE", "PY_COLORS",
] as const;

export const SECRET_RECIPIENTS = ["previews", "tester", "implementer", "fixer", "simplifier", "reviewer"] as const;
export type SecretRecipient = typeof SECRET_RECIPIENTS[number];
export const DEFAULT_SECRET_RECIPIENTS: readonly SecretRecipient[] = ["previews", "tester"];
export const SECRET_RECIPIENTS_REQUIRED = "Give it to at least one, or don't add it yet";
export const secretRecipientsSchema = z.array(z.enum(SECRET_RECIPIENTS)).min(1, SECRET_RECIPIENTS_REQUIRED)
  .refine((names) => new Set(names).size === names.length, "Choose each recipient only once.");
export const changeSecretRecipientsSchema = z.object({ recipients: secretRecipientsSchema }).strict();

/** Each recipient as the settings name it, with what it does. */
export const SECRET_RECIPIENT_OPTIONS: readonly { id: SecretRecipient; label: string; description: string }[] = [
  { id: "previews", label: "Previews", description: "Servers and setup scripts of branch previews" },
  { id: "tester", label: "Tester", description: "Runs tests, including against real services" },
  { id: "implementer", label: "Implementer", description: "Implements the task" },
  { id: "fixer", label: "Fixer", description: "Fixes findings and PR feedback" },
  { id: "simplifier", label: "Simplifier", description: "Simplifies the changes" },
  { id: "reviewer", label: "Reviewer", description: "Reviews the changes" },
];

/** A recipient's label. */
export function secretRecipientLabel(id: SecretRecipient): string {
  return SECRET_RECIPIENT_OPTIONS.find((o) => o.id === id)?.label ?? id;
}

/** dude redacts values at least this many bytes long (orchestrator/internal/redact). */
export const SECRET_REDACT_MIN_BYTES = 6;

/** Said in the dialogs while a value is too short to be redacted. */
export const SHORT_SECRET_WARNING = `Values shorter than ${SECRET_REDACT_MIN_BYTES} characters are not redacted from agent output: they match too much ordinary text.`;

/** Whether dude would leave value unredacted for its length. */
export function secretTooShortToRedact(value: string): boolean {
  return value !== "" && new TextEncoder().encode(value).length < SECRET_REDACT_MIN_BYTES;
}

/** A value's most UTF-8 bytes: well under lux's 8 MiB request and Linux's 128 KiB per variable. */
export const SECRET_VALUE_MAX_BYTES = 32 * 1024;

export const SECRET_NAME_MAX = 63;

export const SECRET_NAME_HELP = "Letters, digits and _, starting with a letter or _. Upper case by convention.";
export const SECRET_VALUE_HELP = "Saved once. dude shows only the last 4 characters after this.";

/** Why a name cannot be a secret at all, whatever the project has; null if it can. */
export function secretNameShapeProblem(name: string): string | null {
  if (!ENV_NAME.test(name)) return "Use letters, digits and _ only, starting with a letter or _.";
  // lux v0.1.11 refuses a secret name past 63 characters (internal/spec nameRe), and with it the whole preview.
  if (name.length > SECRET_NAME_MAX) return `At most ${SECRET_NAME_MAX} characters.`;
  if (/^lux_/i.test(name)) return "Names starting with LUX_ are lux’s own.";
  // GIT_TOKEN_<OWNER> carries a GitHub App's token for each repository owner.
  if (/^GIT_TOKEN(_|$)/.test(name)) return "dude sets GIT_TOKEN and every GIT_TOKEN_ name itself, from the GitHub connection.";
  if ((DUDE_SECRET_NAMES as readonly string[]).includes(name)) return `dude sets ${name} itself.`;
  if (/^(DUDE_|OPENCODE_|CLAUDE_CODE_|GIT_CONFIG_|GIT_AUTHOR_|GIT_COMMITTER_)/.test(name)) return `dude sets ${name} itself.`;
  return null;
}

/** What a recipe needs here: its name and the variables its own env sets. */
export interface RecipeEnvNames {
  name: string;
  env: readonly { name: string }[];
}

/** The project's recipe that sets `name` in its env, if one does (a server's env overrides a Run secret). */
export function recipeSetting(name: string, recipes: readonly RecipeEnvNames[]): RecipeEnvNames | undefined {
  return recipes.find((r) => r.env.some((e) => e.name === name));
}

export function secretClashMessage(server: string, name: string): string {
  return `Server ${server} sets ${name} in its own environment, which would override this. Rename one of them.`;
}

export function duplicateSecretMessage(name: string): string {
  return `There is already a ${name}. Replace its value instead.`;
}

/**
 * Why `name` cannot be added to a project with these secrets and recipes,
 * or null. `kind` says which status the API answers: `invalid` (400) or
 * `conflict` (409).
 */
export function secretNameProblem(
  name: string,
  project: { secrets: readonly string[]; recipes: readonly RecipeEnvNames[] },
): { kind: "invalid" | "conflict"; message: string } | null {
  const shape = secretNameShapeProblem(name);
  if (shape) return { kind: "invalid", message: shape };
  if (project.secrets.includes(name)) return { kind: "conflict", message: duplicateSecretMessage(name) };
  const r = recipeSetting(name, project.recipes);
  if (r) return { kind: "conflict", message: secretClashMessage(r.name, name) };
  return null;
}

/**
 * Why a recipe's env cannot be saved beside the project's secrets: the
 * first variable it sets that is a secret, named with the server; null if
 * none is.
 */
export function recipeSecretClash(recipe: RecipeEnvNames, secrets: readonly string[]): string | null {
  const e = recipe.env.find((v) => secrets.includes(v.name));
  if (!e) return null;
  return `${e.name} is a preview secret of this project, and server ${recipe.name} setting it would override it in previews. Rename one of them.`;
}

/** Why lux (or the container) would not take this value; null if it would. Never quotes the value. */
export function secretValueProblem(value: string): string | null {
  if (value.length === 0) return "Enter a value.";
  if (value.includes("\0")) return "A value cannot contain a NUL character.";
  if (new TextEncoder().encode(value).length > SECRET_VALUE_MAX_BYTES) return "At most 32 KiB.";
  return null;
}

const PEM_END = /\n-----END [^\n]*-----$/;

/**
 * What the API shows of a value: its last 4 characters after trimming
 * whitespace. A PEM block always ends in dashes, so its hint is the last 4
 * characters of the base64 body before its END line.
 */
export function secretHint(value: string): string {
  const v = value.trim().replace(/\r\n/g, "\n");
  const end = v.match(PEM_END);
  if (end) {
    const body = v.slice(0, end.index).split("\n").filter((l) => !l.startsWith("-----") && !l.includes(":")).join("").replace(/\s+/g, "");
    if (body) return Array.from(body).slice(-4).join("");
  }
  return Array.from(v).slice(-4).join("");
}

export const secretNameSchema = z.string().superRefine((n, ctx) => {
  const p = secretNameShapeProblem(n);
  if (p) ctx.addIssue({ code: z.ZodIssueCode.custom, message: p });
});

export const secretValueSchema = z.string().superRefine((v, ctx) => {
  const p = secretValueProblem(v);
  if (p) ctx.addIssue({ code: z.ZodIssueCode.custom, message: p });
});

/** `POST /v1/projects/:id/secrets` */
export const addSecretSchema = z.object({
  name: secretNameSchema,
  value: secretValueSchema,
  recipients: secretRecipientsSchema.default(() => [...DEFAULT_SECRET_RECIPIENTS]),
}).strict();
/** `PUT /v1/projects/:id/secrets/:name` */
export const replaceSecretSchema = z.object({ value: secretValueSchema }).strict();

/** A project's secret, as the API shows it: never its value. */
export interface ProjectSecret {
  name: string;
  recipients: SecretRecipient[];
  hint: string;
  updatedAt: string;
  updatedBy: { id: string; name: string } | null;
}

// ---------------------------------------------------------------------------
// Organisation secrets: project secrets one level up (migration 210). Each
// reaches the agents it is ticked for in every project; a project secret of
// the same name takes its place in that project.
// ---------------------------------------------------------------------------

/** An organisation's secret, as the API shows it: never its value. */
export type OrgSecret = ProjectSecret;

/** An organisation secret as Organisation settings → Secrets lists it. */
export interface OrgSecretListing extends OrgSecret {
  /** Projects holding a secret of the same name, whose agents get that one instead. */
  overriddenIn: { id: string; name: string }[];
  /** Automations that pick it; null for a caller who may not see them (not an admin). */
  pickedBy: { id: string; name: string; projectId: string | null }[] | null;
}

/** `GET /v1/organizations/:id/secrets` */
export interface OrgSecretsResponse {
  organization: { id: string; name: string };
  secrets: OrgSecretListing[];
}

/** An organisation secret may be ticked for nobody: automations that pick it still get it. */
export const orgSecretRecipientsSchema = z.array(z.enum(SECRET_RECIPIENTS))
  .refine((names) => new Set(names).size === names.length, "Choose each recipient only once.");
export const changeOrgSecretRecipientsSchema = z.object({ recipients: orgSecretRecipientsSchema }).strict();

/** `POST /v1/organizations/:id/secrets` */
export const addOrgSecretSchema = z.object({
  name: secretNameSchema,
  value: secretValueSchema,
  recipients: orgSecretRecipientsSchema.default(() => []),
}).strict();

/** Said in the recipients dialog and the table when an organisation secret is ticked for nobody. */
export const ORG_SECRET_NOBODY = "Only automations that pick it";

/** Why `name` cannot be added to the organisation's secrets, or null. */
export function orgSecretNameProblem(name: string, secrets: readonly string[]): { kind: "invalid" | "conflict"; message: string } | null {
  const shape = secretNameShapeProblem(name);
  if (shape) return { kind: "invalid", message: shape };
  if (secrets.includes(name)) return { kind: "conflict", message: duplicateSecretMessage(name) };
  return null;
}

export const SECRET_SOURCES = ["organization", "project"] as const;
export type SecretSource = typeof SECRET_SOURCES[number];

/** An automation's pick of one secret: both levels can hold a name, so the pick names its level. */
export const secretRefSchema = z.object({
  source: z.enum(SECRET_SOURCES),
  name: z.string().regex(/^[A-Za-z_][A-Za-z0-9_]{0,127}$/),
}).strict();
export type SecretRef = z.infer<typeof secretRefSchema>;

export function sameSecretRef(a: SecretRef, b: SecretRef): boolean {
  return a.source === b.source && a.name === b.name;
}

export interface ResolvedSecret<T extends { name: string } = ProjectSecret> {
  source: SecretSource;
  secret: T;
  /** An organisation row whose name the project holds: agents there get the project's. */
  overridden: boolean;
}

/**
 * A project's secrets as its agents get them by name: the project's own
 * first, then the organisation's, each organisation row the project
 * holds a name of marked overridden.
 */
export function resolveSecrets<T extends { name: string }>(org: readonly T[], project: readonly T[]): ResolvedSecret<T>[] {
  const own = new Set(project.map((s) => s.name));
  return [
    ...project.map((secret) => ({ source: "project" as const, secret, overridden: false })),
    ...org.map((secret) => ({ source: "organization" as const, secret, overridden: own.has(secret.name) })),
  ];
}
