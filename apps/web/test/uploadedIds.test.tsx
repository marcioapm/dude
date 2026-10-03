import { expect, test } from "bun:test";
import { withUploadedIds } from "../src/hooks/useTaskImages.tsx";

test("an uploaded image's id replaces the stand-in, and nothing else in the reference changes", () => {
  const text = 'Look ![a\\*b* \\] c](attachment:att_local1 "small right") and ![x](<attachment:att_local2> \'320 left\'), ![keep](attachment:att_real)';
  const out = withUploadedIds(text, new Map([["att_local1", "att_R1"], ["att_local2", "att_R2"]]));
  expect(out).toBe('Look ![a\\*b* \\] c](attachment:att_R1 "small right") and ![x](<attachment:att_R2> \'320 left\'), ![keep](attachment:att_real)');
});
