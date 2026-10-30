/** LiveDiff's view switch drawn into a page's bar sits at the bar's `toolbar` size. */

import { describe, expect, mock, test } from "bun:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { cssModuleAsWritten } from "./cssModule.ts";

// Segmented's class names as written, so its size can be read.
mock.module(...cssModuleAsWritten(new URL("../src/components/ScreenHeader.module.css", import.meta.url)));
const { LiveDiff } = await import("../src/components/LiveDiff.tsx");

const files = [{ path: "a.ts", status: "M" as const, additions: 1, deletions: 0,
  hunks: [{ header: "@@ -1 +1 @@", lines: [{ kind: "+" as const, old: null, new: 1, text: "x" }] }] }];

describe("LiveDiff's view switch", () => {
  test("is toolbar size in the page's bar, small in its own", () => {
    const host = document.createElement("div");
    const bar = document.createElement("div");
    document.body.append(host, bar);
    const root = createRoot(host);
    try {
      act(() => root.render(<LiveDiff base="b" files={files} toolbarIn={bar} />));
      expect(bar.querySelector('[data-testid="diff-view"]')!.className).toContain("segToolbar");
      act(() => root.render(<LiveDiff base="b" files={files} />));
      expect(host.querySelector('[data-testid="diff-view"]')!.className).toContain("segSm");
      expect(host.querySelector('[data-testid="diff-view"]')!.className).not.toContain("segToolbar");
    } finally {
      act(() => root.unmount());
      host.remove();
      bar.remove();
    }
  });
});
