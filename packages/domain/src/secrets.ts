import { z } from "zod";
import { ENV_NAME } from "./servers.ts";

/**
 * Preview secrets: a project's environment variables that every branch
 * preview gets as lux secrets (`as: env`), and no agent ever does. Written
 * once and never shown again: the API answers with a hint, the value's last
 * characters. Shared by the API and the web app; the orchestrator reads the
 * values (orchestrator/internal/servers).
 */

/**
 * Names dude declares as lux secrets of its own (orchestrator/internal/phases):
 * a preview declaring one twice is refused by lux.
 */
export const DUDE_SECRET_NAMES = ["GIT_TOKEN", "DUDE_TOOLS_AUTH", "DUDE_REGISTRY_AUTH"] as const;

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
  if (name === "GIT_TOKEN") return "dude sets GIT_TOKEN itself, from the GitHub connection.";
  if ((DUDE_SECRET_NAMES as readonly string[]).includes(name)) return `dude sets ${name} itself.`;
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
export const addSecretSchema = z.object({ name: secretNameSchema, value: secretValueSchema }).strict();
/** `PUT /v1/projects/:id/secrets/:name` */
export const replaceSecretSchema = z.object({ value: secretValueSchema }).strict();

/** A project's secret, as the API shows it: never its value. */
export interface PreviewSecret {
  name: string;
  hint: string;
  updatedAt: string;
  updatedBy: { id: string; name: string } | null;
}
