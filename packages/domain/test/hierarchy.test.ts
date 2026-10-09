import { describe, expect, test } from "bun:test";
import {
  ALL_AGENT_ROLES,
  agentModelConfigSchema,
  clampTimeLimit,
  resolveAgentModel,
  ROLE_MODEL_REMOVED,
  taskGoalShortBy,
  type AgentModels,
  type Organization,
  type Project,
} from "../src/hierarchy.ts";

const org = (models: AgentModels) =>
  ({ defaultAgentModels: models }) as Pick<Organization, "defaultAgentModels">;
const project = (models: AgentModels) => ({ agentModels: models }) as Pick<Project, "agentModels">;

describe("resolveAgentModel", () => {
  test("prefers the project binding over the organization default", () => {
    const resolved = resolveAgentModel(
      "conductor",
      project({ conductor: { tier: "project-tier" } }),
      org({ conductor: { tier: "org-tier" } }),
    );
    expect(resolved?.tier).toBe("project-tier");
  });

  test("falls back to the organization default when the project is silent", () => {
    const resolved = resolveAgentModel("reviewer", project({}), org({ reviewer: { tier: "org-tier" } }));
    expect(resolved?.tier).toBe("org-tier");
  });

  test("falls back to system defaults last", () => {
    const resolved = resolveAgentModel("simplifier", project({}), org({}), {
      simplifier: { tier: "system-tier" },
    });
    expect(resolved?.tier).toBe("system-tier");
  });

  test("resolves field by field: a project's time limit keeps the organization's tier", () => {
    const resolved = resolveAgentModel(
      "reviewer",
      project({ reviewer: { timeLimitMinutes: 45 } }),
      org({ reviewer: { tier: "org-tier", timeLimitMinutes: 20, context: "org notes" } }),
    );
    expect(resolved).toEqual({ tier: "org-tier", timeLimitMinutes: 45, context: "org notes" });
  });

  test("returns null when no layer configures the role", () => {
    expect(resolveAgentModel("qa_browser", project({}), org({}))).toBeNull();
  });

  test("resolves each role independently", () => {
    const resolved = resolveAgentModel(
      "implementer",
      project({ conductor: { tier: "project-conductor" } }),
      org({ implementer: { tier: "org-implementer" } }),
    );
    // The project configures a *different* role, so it must not shadow this one.
    expect(resolved?.tier).toBe("org-implementer");
  });

  test("every declared role is resolvable", () => {
    const models = Object.fromEntries(
      ALL_AGENT_ROLES.map((role) => [role, { tier: `tier-${role}` }]),
    ) as AgentModels;
    for (const role of ALL_AGENT_ROLES) {
      expect(resolveAgentModel(role, project(models), org({}))?.tier).toBe(`tier-${role}`);
    }
  });
});

describe("agentModelConfigSchema", () => {
  test("a tier, when given, is not empty — and a layer may give none", () => {
    expect(agentModelConfigSchema.safeParse({ tier: "" }).success).toBe(false);
    expect(agentModelConfigSchema.safeParse({}).success).toBe(true);
  });

  test("a role names a tier, never a model: a model is refused, saying so", () => {
    const parsed = agentModelConfigSchema.safeParse({ model: "llm-anthropic/claude-opus-5-5", tier: "mtr_x" });
    expect(parsed.success).toBe(false);
    expect(parsed.error?.issues[0]?.message).toBe(ROLE_MODEL_REMOVED);
  });

  test("accepts optional tuning and cost fields", () => {
    const parsed = agentModelConfigSchema.parse({
      tier: "mtr_x",
      harness: "opencode",
      maxTokens: 1000,
      temperature: 0.2,
      costLimitUsd: 5,
    });
    expect(parsed.harness).toBe("opencode");
    expect(parsed.costLimitUsd).toBe(5);
  });

  test("rejects out-of-range tuning values", () => {
    expect(agentModelConfigSchema.safeParse({ tier: "mtr_x", temperature: 3 }).success).toBe(false);
    expect(agentModelConfigSchema.safeParse({ tier: "mtr_x", maxTokens: 0 }).success).toBe(false);
    expect(agentModelConfigSchema.safeParse({ tier: "mtr_x", costLimitUsd: -1 }).success).toBe(false);
  });
});

describe("taskGoalShortBy", () => {
  test("counts UTF-16 units of the goal without its surrounding whitespace", () => {
    expect(taskGoalShortBy("")).toBe(16);
    expect(taskGoalShortBy("a".repeat(15))).toBe(1);
    expect(taskGoalShortBy("a".repeat(16))).toBe(0);
    expect(taskGoalShortBy(`  \n${"a".repeat(15)}\t `)).toBe(1);
    expect(taskGoalShortBy(`${"😀".repeat(7)}a`)).toBe(1);
    expect(taskGoalShortBy("😀".repeat(8))).toBe(0);
  });

  // The agents' create_task trims the same set; these are where it differs from Go's TrimSpace.
  test("trims ECMAScript whitespace: a BOM is trimmed, a NEL is counted", () => {
    expect(taskGoalShortBy(`\uFEFF${"a".repeat(15)}\uFEFF`)).toBe(1);
    expect(taskGoalShortBy(" ".repeat(20))).toBe(16);
    expect(taskGoalShortBy(`\u0085${"a".repeat(15)}`)).toBe(0);
  });
});

describe("a role's time limit without progress", () => {
  test("is half an hour at least and a week at most; a stored lower one reads as half an hour", () => {
    const limit = (timeLimitMinutes: number) => agentModelConfigSchema.safeParse({ timeLimitMinutes }).success;
    expect([limit(29), limit(30), limit(10_080), limit(10_081)]).toEqual([false, true, true, false]);
    expect([clampTimeLimit(10), clampTimeLimit(30), clampTimeLimit(45)]).toEqual([30, 30, 45]);
  });
});
