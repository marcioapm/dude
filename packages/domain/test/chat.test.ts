import { expect, test } from "bun:test";
import {
  CHAT_EPIC_TASKS_MAX,
  chatOriginSchema,
  chatProviderSchema,
  chatRouteSchema,
  epicTasksProblem,
  routeMadeSomething,
} from "../src/chat.ts";

test("an epic's tasks are refused for a cycle, a dangling ref, a repeated ref or more than 12", () => {
  const t = (ref: string, ...blockedBy: string[]) => ({ ref, blockedBy });
  expect(epicTasksProblem([t("t1"), t("t2", "t1"), t("t3", "t1"), t("t4", "t2", "t3")])).toBeNull();
  expect(epicTasksProblem([t("t1", "t2"), t("t2", "t1")])).toContain("cycle");
  expect(epicTasksProblem([t("t1", "t1")])).toContain("cycle");
  expect(epicTasksProblem([t("t1", "t9")])).toContain("unknown t9");
  expect(epicTasksProblem([t("t1"), t("t1")])).toContain("repeated");
  expect(epicTasksProblem([])).toContain("at least one");
  const many = Array.from({ length: CHAT_EPIC_TASKS_MAX + 1 }, (_, i) => t(`t${i}`));
  expect(epicTasksProblem(many)).toContain("at most 12");
  expect(epicTasksProblem(many.slice(0, CHAT_EPIC_TASKS_MAX))).toBeNull();
});

test("routes: refusals and failures made nothing; the made and linked ones did", () => {
  expect(chatRouteSchema.options.filter(routeMadeSomething)).toEqual(["brainstorm", "task", "epic", "resume", "task_link"]);
  expect(chatRouteSchema.safeParse("not_installed").success).toBe(false);
});

test("a provider is an id, a name and a glyph; an origin names where the thread was", () => {
  expect(chatProviderSchema.safeParse({ id: "slack", name: "Slack", glyph: "slack", workspaceWord: "workspace" }).success).toBe(true);
  expect(chatProviderSchema.safeParse({ id: "Slack", name: "Slack", glyph: "slack", workspaceWord: "workspace" }).success).toBe(false);
  expect(chatOriginSchema.safeParse({ provider: "slack", workspaceId: "T1", conversationId: "C1", conversationName: "#support",
    threadId: "1.0", permalink: "https://example.com/p", requestedBy: "Márcia" }).success).toBe(true);
  expect(chatOriginSchema.safeParse({ provider: "slack" }).success).toBe(false);
});
