import { expect, test } from "bun:test";
import { EventTypes } from "../src/events/types.ts";
import shared from "./child-event-types.json";

// The same rows are read by orchestrator/internal/phases/children_types_test.go:
// the translator's constants and the vocabulary the browser reads agree.
test("the child-session event types are the ones the orchestrator writes", () => {
  for (const [name, value] of Object.entries(shared)) {
    expect(EventTypes[name as keyof typeof EventTypes] as string).toBe(value);
  }
});
