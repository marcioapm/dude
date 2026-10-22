import { expect, test } from "bun:test";
import { EventTypes } from "../src/events/types.ts";
import shared from "./review-question-event-types.json";

// A vocabulary check, not behaviour: the same rows are read by
// orchestrator/internal/delivery/review_question_events_test.go, so a type
// renamed on one side only fails.
test("the reviewer-question event type names equal the orchestrator's constants, by the shared fixture", () => {
  for (const [name, value] of Object.entries(shared)) {
    expect(EventTypes[name as keyof typeof EventTypes] as string).toBe(value);
  }
});
