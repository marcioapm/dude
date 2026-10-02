import { expect, test } from "bun:test";
import { join } from "node:path";

// The dev agent image's OpenCode catalog. OpenCode takes a custom provider's
// model as text-only unless its entry says otherwise, and then refuses the
// images a steer, an answer or a task's prompt carries.
const CATALOG = join(import.meta.dir, "../../../images/runtime/opencode.json");

type Model = { attachment?: unknown; modalities?: { input?: unknown; output?: unknown } };

test("every model in the agent image's OpenCode catalog takes images", async () => {
  const config = (await Bun.file(CATALOG).json()) as { provider: Record<string, { models: Record<string, Model> }> };
  const models = Object.entries(config.provider).flatMap(([provider, p]) =>
    Object.entries(p.models).map(([name, m]) => [`${provider}/${name}`, m] as const),
  );
  expect(models.length).toBeGreaterThan(0);
  const textOnly = models
    .filter(([, m]) => !(m.attachment === true && Bun.deepEquals(m.modalities, { input: ["text", "image"], output: ["text"] })))
    .map(([name]) => name);
  expect(textOnly).toEqual([]);
});
