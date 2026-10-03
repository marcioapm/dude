/**
 * Lines are for fields and focus, not for separating things: regions are
 * told apart by shade and space. A border survives only where it is the
 * thing's shape or meaning — a field's edge, a checkbox, a diff's gutter, a
 * bar down one side that carries a tone or a thread — and each one is listed
 * here with why. A hairline between rows, round a card, or under a header is
 * how the screen fills with boxes again, so anything unlisted fails.
 */

import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { declarations, ROOT, stylesheets } from "./stylesheets.ts";

/** `file` → selectors whose border is allowed, and why. */
const ALLOWED: Record<string, Record<string, string>> = {
  "packages/design-system/src/primitives/Input.module.css": { "*": "a field's edge" },
  "packages/design-system/src/primitives/Textarea.module.css": { "*": "a field's edge" },
  "packages/design-system/src/primitives/MarkdownEditor.module.css": {
    ".frame": "a field's edge",
    ".frame:hover": "a field's edge",
    ".frame:focus-within": "focus",
    '.frame[data-invalid="true"]': "a field's edge, invalid",
    '.frame[data-invalid="true"]:focus-within': "focus, invalid",
    '.frame[data-locked="true"], .frame[data-locked="true"]:hover': "a field's edge, fixed",
  },
  "packages/design-system/src/primitives/Select.module.css": { "*": "a field's edge" },
  "packages/design-system/src/primitives/NumberInput.module.css": { "*": "a field's edge" },
  "packages/design-system/src/components/CodeEditor.module.css": {
    ".frame": "a field's edge",
    ".frame:hover": "a field's edge",
    ".frame:focus-within": "focus",
  },
  "packages/design-system/src/components/ImagePicker.module.css": {
    ".field": "a field's edge",
    ".field:hover": "a field's edge",
    ".field:focus-within, .fieldOpen": "focus",
  },
  "packages/design-system/src/primitives/Checkbox.module.css": { "*": "a checkbox is its outline" },
  "packages/design-system/src/primitives/ChoiceList.module.css": { ".dot": "a radio is its outline", ".chosen .dot": "a radio is its outline" },
  "packages/design-system/src/components/ChatComposer.module.css": { ".field": "a field's edge", ".field:hover": "a field's edge", ".field:focus-within": "focus" },
  "packages/design-system/src/components/Sidebar.module.css": { ".search": "a field's edge", ".search:hover": "a field's edge", ".search:focus-within": "focus" },
  "packages/design-system/src/components/DiffView.module.css": { ".gutter": "a diff's gutter" },
  "packages/design-system/src/components/MarkdownDocument.module.css": { ".editing": "a field's edge, while it is one" },
  "packages/design-system/src/components/Markdown.module.css": {
    ".hr": "a document's own rule",
    ".taskMark": "a checkbox is its outline",
    ".taskDone .taskMark": "a checkbox is its outline",
  },
  "packages/design-system/src/components/StatusBadge.module.css": { ".dot.pending .dotMark": "a hollow dot is its outline" },
  "packages/design-system/src/primitives/Tabs.module.css": {
    ".trigger": "the selected tab's underline",
    '.trigger[data-state="active"]': "the selected tab's underline",
  },
  // Bars down one side that carry meaning, not separation.
  "packages/design-system/src/components/ChatMessage.module.css": { ".quote": "a quotation's bar" },
  "packages/design-system/src/components/ChatThread.module.css": { ".root": "a subagent's thread, in its role's colour", ".finished": "a finished thread, fading" },
  "packages/design-system/src/components/ChatRunLine.module.css": { ".root": "a Run the conductor started, in its role's colour" },
  "packages/design-system/src/components/EventRow.module.css": { ".row": "the event's tone", ".detail": "the event's tone" },
  "packages/design-system/src/components/ThinkingBlock.module.css": { ".body": "the thinking rail" },
  "packages/design-system/src/components/ToolCallCard.module.css": { ".preStderr": "stderr's tone", ".badExit .preStderr": "stderr's tone" },
  "packages/design-system/src/primitives/Toast.module.css": { ".root": "the toast's tone" },
  "packages/design-system/src/styles/base.css": { "::-webkit-scrollbar-thumb": "transparent padding round the thumb" },
};

/** A border that draws nothing. */
const INVISIBLE = /^(0|none|transparent|0 none|1px solid transparent|2px solid transparent)$/;
/** A custom property holding a colour is not a border. */
const PROP = /^border(-(top|bottom|left|right|inline(-start|-end)?|block(-start|-end)?))?(-color|-style|-width)?$/;

describe("borders", () => {
  for (const path of stylesheets()) {
    test(`${path} draws lines only where they mean something`, () => {
      const allowed = ALLOWED[path] ?? {};
      const bad = declarations(readFileSync(`${ROOT}/${path}`, "utf8"), PROP)
        .filter(([selector, , value]) => !INVISIBLE.test(value) && !("*" in allowed) && !(selector in allowed))
        .map(([selector, prop, value]) => `${selector} { ${prop}: ${value} }`);
      expect(bad).toEqual([]);
    });
  }

  test("every allowed border still exists", () => {
    for (const [path, selectors] of Object.entries(ALLOWED)) {
      if ("*" in selectors) continue;
      const found = new Set(declarations(readFileSync(`${ROOT}/${path}`, "utf8"), PROP).map(([s]) => s));
      for (const selector of Object.keys(selectors)) expect(found.has(selector), `${path} ${selector}`).toBe(true);
    }
  });
});
