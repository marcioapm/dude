import { expect, test } from "bun:test";
import { POLICY_WHY } from "../src/hierarchy.ts";
import shared from "./policy-why.json";

// A vocabulary check, not behaviour: the same rows are read by
// orchestrator/internal/delivery/policy_why_test.go, so a why reworded on
// one side only fails.
test("the whys Deliver's policy takes a delivery for equal the orchestrator's constants, by the shared fixture", () => {
  expect(POLICY_WHY).toEqual(shared);
});
