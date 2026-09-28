import { describe, expect, test } from "bun:test";
import {
  ALL_AGENT_ROLES,
  agentModelConfigSchema,
  resolveAgentModel,
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
      project({ orchestrator: { model: "project-model" } }),
      org({ orchestrator: { model: "org-model" } }),
    );
    expect(resolved?.model).toBe("project-model");
  });

  test("falls back to the organization default when the project is silent", () => {
    const resolved = resolveAgentModel("reviewer", project({}), org({ reviewer: { model: "org-model" } }));
    expect(resolved?.model).toBe("org-model");
  });

  test("falls back to system defaults last", () => {
    const resolved = resolveAgentModel("simplifier", project({}), org({}), {
      simplifier: { model: "system-model" },
    });
    expect(resolved?.model).toBe("system-model");
  });

  test("resolves field by field: a project's effort keeps the organization's model", () => {
    const resolved = resolveAgentModel(
      "reviewer",
      project({ reviewer: { effort: "low" } }),
      org({ reviewer: { model: "org-model", effort: "high", timeLimitMinutes: 20 } }),
    );
    expect(resolved).toEqual({ model: "org-model", effort: "low", timeLimitMinutes: 20 });
  });

  test("returns null when no layer configures the role", () => {
    expect(resolveAgentModel("qa_browser", project({}), org({}))).toBeNull();
  });

  test("resolves each role independently", () => {
    const resolved = resolveAgentModel(
      "implementer",
      project({ orchestrator: { model: "project-orchestrator" } }),
      org({ implementer: { model: "org-implementer" } }),
    );
    // The project configures a *different* role, so it must not shadow this one.
    expect(resolved?.model).toBe("org-implementer");
  });

  test("every declared role is resolvable", () => {
    const models = Object.fromEntries(
      ALL_AGENT_ROLES.map((role) => [role, { model: `model-${role}` }]),
    ) as AgentModels;
    for (const role of ALL_AGENT_ROLES) {
      expect(resolveAgentModel(role, project(models), org({}))?.model).toBe(`model-${role}`);
    }
  });
});

describe("agentModelConfigSchema", () => {
  test("a model, when given, is not empty — and a layer may give none", () => {
    expect(agentModelConfigSchema.safeParse({ model: "" }).success).toBe(false);
    expect(agentModelConfigSchema.safeParse({}).success).toBe(true);
    expect(agentModelConfigSchema.safeParse({ effort: "high" }).success).toBe(true);
    expect(agentModelConfigSchema.safeParse({ effort: "extreme" }).success).toBe(false);
  });

  test("accepts optional tuning and cost fields", () => {
    const parsed = agentModelConfigSchema.parse({
      model: "m",
      harness: "opencode",
      maxTokens: 1000,
      temperature: 0.2,
      costLimitUsd: 5,
    });
    expect(parsed.harness).toBe("opencode");
    expect(parsed.costLimitUsd).toBe(5);
  });

  test("rejects out-of-range tuning values", () => {
    expect(agentModelConfigSchema.safeParse({ model: "m", temperature: 3 }).success).toBe(false);
    expect(agentModelConfigSchema.safeParse({ model: "m", maxTokens: 0 }).success).toBe(false);
    expect(agentModelConfigSchema.safeParse({ model: "m", costLimitUsd: -1 }).success).toBe(false);
  });
});
