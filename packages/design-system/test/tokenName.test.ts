import { describe, expect, test } from "bun:test";
import { cssVar, densityTokens, type DensityToken } from "../src/tokens/index.ts";

// The `@ts-expect-error` lines are the test: `bun run typecheck` fails if a
// misspelt name ever type-checks again.
describe("cssVar names", () => {
  test("rejects misspelt tokens at compile time", () => {
    // @ts-expect-error misspelt named space
    cssVar("space-mian-pad");
    // @ts-expect-error misspelt size
    cssVar("size-avtar-chat");
    // @ts-expect-error misspelt density-only token
    cssVar("leading-chta");
    // @ts-expect-error camelCase scale key instead of its CSS name
    cssVar("space-chatGap");
  });

  test("accepts density-only tokens", () => {
    const names = Object.keys(densityTokens.comfortable) as DensityToken[];
    for (const n of names) expect(cssVar(n)).toBe(`var(--ds-${n})`);
    expect(cssVar("leading-chat")).toBe("var(--ds-leading-chat)");
    expect(cssVar("md-gap", "1em")).toBe("var(--ds-md-gap, 1em)");
    expect(cssVar("space-chat-avatar-gap")).toBe("var(--ds-space-chat-avatar-gap)");
    expect(cssVar("size-avatar-chat")).toBe("var(--ds-size-avatar-chat)");
  });
});
