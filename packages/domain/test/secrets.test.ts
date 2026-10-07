import { describe, expect, test } from "bun:test";
import {
  addSecretSchema,
  recipeInputSchema,
  recipeSecretClash,
  secretHint,
  secretNameProblem,
  secretValueProblem,
  SECRET_VALUE_MAX_BYTES,
} from "../src/index.ts";

const project = {
  secrets: ["SEED_LLM_KEY"],
  recipes: [
    { name: "web", env: [{ name: "PORT" }, { name: "NODE_ENV" }] },
    { name: "api", env: [] },
  ],
};

describe("a secret's name", () => {
  test("an environment variable's name, as a recipe's", () => {
    expect(secretNameProblem("STRIPE_TEST_KEY", project)).toBeNull();
    expect(secretNameProblem("_x1", project)).toBeNull();
    expect(secretNameProblem("SEED-LLM-KEY", project)).toEqual({ kind: "invalid", message: "Use letters, digits and _ only, starting with a letter or _." });
    expect(secretNameProblem("1KEY", project)?.kind).toBe("invalid");
    expect(secretNameProblem("", project)?.kind).toBe("invalid");
  });

  test("LUX_ is refused in any letter case", () => {
    for (const n of ["LUX_TOKEN", "lux_token", "Lux_Token"]) {
      expect(secretNameProblem(n, project)).toEqual({ kind: "invalid", message: "Names starting with LUX_ are lux’s own." });
    }
    expect(secretNameProblem("LUXURY", project)).toBeNull();
  });

  test("the names dude sets itself are refused", () => {
    expect(secretNameProblem("GIT_TOKEN", project)).toEqual({ kind: "invalid", message: "dude sets GIT_TOKEN itself, from the GitHub connection." });
    expect(secretNameProblem("DUDE_TOOLS_AUTH", project)?.kind).toBe("invalid");
    // A preview's registry login, beside its own secrets.
    expect(secretNameProblem("DUDE_REGISTRY_AUTH", project)).toEqual({ kind: "invalid", message: "dude sets DUDE_REGISTRY_AUTH itself." });
  });

  test("at most 63 characters, as lux takes a secret's name", () => {
    expect(secretNameProblem("A".repeat(63), project)).toBeNull();
    expect(secretNameProblem("A".repeat(64), project)).toEqual({ kind: "invalid", message: "At most 63 characters." });
    expect(addSecretSchema.safeParse({ name: "A".repeat(64), value: "v" }).success).toBe(false);
  });

  test("a name the project has is a conflict", () => {
    expect(secretNameProblem("SEED_LLM_KEY", project)).toEqual({ kind: "conflict", message: "There is already a SEED_LLM_KEY. Replace its value instead." });
  });

  test("a name a server sets in its own env is a conflict naming the server", () => {
    expect(secretNameProblem("PORT", project)).toEqual({
      kind: "conflict",
      message: "Server web sets PORT in its own environment, which would override this. Rename one of them.",
    });
  });

  test("the API's schema refuses what the rules refuse", () => {
    expect(addSecretSchema.safeParse({ name: "lux_x", value: "v" }).success).toBe(false);
    expect(addSecretSchema.safeParse({ name: "GIT_TOKEN", value: "v" }).success).toBe(false);
    expect(addSecretSchema.safeParse({ name: "OK", value: "v" }).success).toBe(true);
  });
});

describe("a recipe's env beside the project's secrets", () => {
  test("a recipe setting a secret's name is refused, naming the server and the variable", () => {
    const msg = recipeSecretClash({ name: "web", env: [{ name: "PORT" }, { name: "SEED_LLM_KEY" }] }, ["SEED_LLM_KEY"]);
    expect(msg).toContain("server web");
    expect(msg).toContain("SEED_LLM_KEY");
    expect(recipeSecretClash({ name: "web", env: [{ name: "PORT" }] }, ["SEED_LLM_KEY"])).toBeNull();
  });

  test("a recipe's env refuses LUX_ in any letter case", () => {
    const recipe = (name: string) => recipeInputSchema.safeParse({ name: "web", port: 3000, command: "x", env: [{ name, value: "1" }] });
    expect(recipe("LUX_X").success).toBe(false);
    expect(recipe("lux_x").success).toBe(false);
    expect(recipe("LUXE").success).toBe(true);
  });
});

describe("a secret's value", () => {
  test("non-empty, no NUL, at most 32 KiB, kept as given", () => {
    expect(secretValueProblem("")).not.toBeNull();
    expect(secretValueProblem("a\0b")).not.toBeNull();
    expect(secretValueProblem("x".repeat(SECRET_VALUE_MAX_BYTES))).toBeNull();
    expect(secretValueProblem("x".repeat(SECRET_VALUE_MAX_BYTES + 1))).not.toBeNull();
    // Bytes, not characters: 'é' is two.
    expect(secretValueProblem("é".repeat(SECRET_VALUE_MAX_BYTES / 2 + 1))).not.toBeNull();
    const parsed = addSecretSchema.parse({ name: "K", value: "  line 1\nline 2\n" });
    expect(parsed.value).toBe("  line 1\nline 2\n");
  });

  test("whitespace alone is a value lux takes", () => {
    expect(secretValueProblem(" ")).toBeNull();
  });
});

describe("a secret's hint", () => {
  test("the last 4 characters", () => {
    expect(secretHint("sk-test-abcd3f9a")).toBe("3f9a");
    expect(secretHint("ab")).toBe("ab");
  });

  test("after trimming whitespace", () => {
    expect(secretHint("sk-test-3f9a\n")).toBe("3f9a");
    expect(secretHint("  sk-test-3f9a  \r\n")).toBe("3f9a");
  });

  test("a PEM block's is its body's, not the END line's dashes", () => {
    const pem = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\nk3Lq9bF0rT2yVf8mWJxQ1s0ZpN4cD6eH5aR7uGvKtYwE9iL3oM\n-----END PRIVATE KEY-----\n";
    expect(secretHint(pem)).toBe("L3oM");
    expect(secretHint(pem.replace(/\n/g, "\r\n"))).toBe("L3oM");
    // A last body line shorter than 4 reaches back into the line before.
    expect(secretHint("-----BEGIN X-----\nABCDEF\nGH\n-----END X-----")).toBe("EFGH");
    // Padding is part of the body.
    expect(secretHint("-----BEGIN X-----\nQUJD==\n-----END X-----")).toBe("JD==");
  });
});
