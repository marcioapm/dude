/**
 * A repository's setup script pieces: the mark after its name, the byte
 * meter in its editor's footer, and the editor's line cap while it loads.
 * CodeMirror itself (shell highlighting, the placeholder) is a browser's:
 * the e2e settings suite drives it.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { SetupScriptMark, SizeMeter, sizeLevel, textLines } from "../src/components/SetupScript.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("SizeMeter", () => {
  test("neutral to 90% of the limit, attention past it, danger only over it", () => {
    expect(sizeLevel(0, 1000)).toBe("neutral");
    expect(sizeLevel(900, 1000)).toBe("neutral");
    expect(sizeLevel(901, 1000)).toBe("attention");
    expect(sizeLevel(1000, 1000)).toBe("attention");
    expect(sizeLevel(1001, 1000)).toBe("danger");
  });

  test("says the size against the limit in the byte formatter's units", () => {
    const html = renderToStaticMarkup(<SizeMeter bytes={284} limit={16_384} />);
    expect(html).toContain("284 B of 16 KB");
    expect(html).toContain('data-level="neutral"');
    expect(renderToStaticMarkup(<SizeMeter bytes={16_385} limit={16_384} />)).toContain('data-level="danger"');
  });
});

describe("textLines", () => {
  test("counts lines as written, a closing newline starting none", () => {
    expect(textLines("")).toBe(0);
    expect(textLines("a")).toBe(1);
    expect(textLines("a\nb\n")).toBe(2);
    expect(textLines("a\n\nb")).toBe(3);
  });
});

describe("SetupScriptMark", () => {
  test("is a button whose name says the repository, the action and the lines", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    let opened = 0;
    await act(async () => root!.render(
      <TooltipProvider>
        <SetupScriptMark repository="api" lines={9} action="edit" onClick={() => opened++} />
        <SetupScriptMark repository="web" lines={1} action="view" />
      </TooltipProvider>,
    ));
    const [edit, view] = [...host.querySelectorAll<HTMLButtonElement>("[data-testid=setup-script-mark]")];
    expect(edit!.tagName).toBe("BUTTON");
    expect(edit!.getAttribute("aria-label")).toBe("Edit api’s setup script, 9 lines");
    expect(view!.getAttribute("aria-label")).toBe("Read web’s setup script, 1 line");
    expect(edit!.textContent).toBe("setup");
    await act(async () => edit!.click());
    expect(opened).toBe(1);
  });
});
