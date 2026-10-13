import { expect, test } from "bun:test";
import { EventTypes } from "../src/events/types.ts";
import shared from "./child-event-types.json";

// A vocabulary check, not behaviour: the same rows are read by
// orchestrator/internal/phases/children_types_test.go, so a type renamed on
// one side only fails. What each event carries is pinned by the translator's
// replay tests and apps/web/test/children.test.ts.
test("the child-session event type names equal the orchestrator's constants, by the shared fixture", () => {
  for (const [name, value] of Object.entries(shared)) {
    expect(EventTypes[name as keyof typeof EventTypes] as string).toBe(value);
  }
});
