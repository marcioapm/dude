import { describe, expect, test } from "bun:test";
import {
  ALL_AGENT_ROLES,
  agentModelConfigSchema,
  resolveAgentModel,
  ROLE_MODEL_REMOVED,
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
      "orchestrator",
      project({ orchestrator: { tier: "project-tier" } }),
      org({ orchestrator: { tier: "org-tier" } }),
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

  test("resolves field by field: a project's effort keeps the organization's tier", () => {
    const resolved = resolveAgentModel(
      "reviewer",
      project({ reviewer: { effort: "low" } }),
      org({ reviewer: { tier: "org-tier", effort: "high", timeLimitMinutes: 20 } }),
    );
    expect(resolved).toEqual({ tier: "org-tier", effort: "low", timeLimitMinutes: 20 });
  });

  test("returns null when no layer configures the role", () => {
    expect(resolveAgentModel("qa_browser", project({}), org({}))).toBeNull();
  });

  test("resolves each role independently", () => {
    const resolved = resolveAgentModel(
      "implementer",
      project({ orchestrator: { tier: "project-orchestrator" } }),
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
    expect(agentModelConfigSchema.safeParse({ effort: "high" }).success).toBe(true);
    expect(agentModelConfigSchema.safeParse({ effort: "extreme" }).success).toBe(false);
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
