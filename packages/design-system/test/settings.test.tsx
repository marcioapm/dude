import { describe, expect, test } from "bun:test";
import { lineDiff } from "../src/util/lineDiff.ts";

describe("lineDiff", () => {
  test("identical texts have no hunks", () => {
    expect(lineDiff("a\nb", "a\nb")).toEqual({ hunks: [], additions: 0, deletions: 0 });
  });

  test("a changed line is a deletion and an addition, with context round it", () => {
    const before = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "10"].join("\n");
    const after = before.replace("5", "five");
    const d = lineDiff(before, after);
    expect([d.additions, d.deletions]).toEqual([1, 1]);
    expect(d.hunks).toHaveLength(1);
    const kinds = d.hunks[0]!.lines.map((l) => `${l.kind}:${l.text}`);
    expect(kinds).toEqual(["context:2", "context:3", "context:4", "del:5", "add:five", "context:6", "context:7", "context:8"]);
    expect(d.hunks[0]!.header).toBe("@@ -2,7 +2,7 @@");
  });

  test("changes far apart are separate hunks", () => {
    const before = Array.from({ length: 30 }, (_, n) => `line ${n}`).join("\n");
    const after = before.replace("line 2\n", "line two\n").replace("line 27", "line twenty-seven");
    expect(lineDiff(before, after).hunks).toHaveLength(2);
  });

  test("from nothing, everything is added", () => {
    const d = lineDiff("", "a\nb");
    expect([d.additions, d.deletions]).toEqual([2, 0]);
    expect(d.hunks[0]!.lines.map((l) => l.newNo)).toEqual([1, 2]);
  });
});

import { renderToStaticMarkup } from "react-dom/server";
import { MarkdownDocument } from "../src/components/MarkdownDocument.tsx";
import { SettingSource, SettingsLayout } from "../src/components/Settings.tsx";
import { EpicProgress } from "../src/components/EpicCard.tsx";

const text = (h: string) => h.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();

describe("MarkdownDocument", () => {
  test("reads rendered, with Edit only when it can be saved", () => {
    const h = renderToStaticMarkup(<MarkdownDocument source={"# Title\n\nBody"} onSave={() => {}} />);
    expect(h).toContain("<h1");
    expect(h).toContain('data-testid="markdown-edit"');
    expect(h).not.toContain("<textarea");
    expect(renderToStaticMarkup(<MarkdownDocument source="Body" />)).not.toContain("markdown-edit");
  });

  test("editing is the source, with Save and Cancel", () => {
    const h = renderToStaticMarkup(<MarkdownDocument source={"# Title"} onSave={() => {}} defaultEditing />);
    expect(h).toContain("<textarea");
    expect(h).toContain(">Cancel<");
    expect(h).toContain(">Save<");
    expect(h).not.toContain("<h1");
  });

  test("an empty document says so", () => {
    expect(text(renderToStaticMarkup(<MarkdownDocument source="" emptyText="Nothing added." />))).toContain("Nothing added.");
  });
});

describe("SettingSource", () => {
  test("inherited says where from; overridden says what it overrides, and resets", () => {
    expect(text(renderToStaticMarkup(<SettingSource source="organization" from="Acme" />))).toBe("From Acme");
    const h = renderToStaticMarkup(<SettingSource source="project" from="Acme" inherited="off" onReset={() => {}} />);
    expect(text(h)).toBe("Overridden Acme: off Reset");
    expect(h).toContain("<button");
  });
});

describe("SettingsLayout", () => {
  test("a page's sub-pages show while it is current", () => {
    const items = [{ id: "agents", label: "Agents", items: [{ id: "implementer", label: "Implementer" }] }, { id: "delivery", label: "Delivery" }];
    const open = renderToStaticMarkup(<SettingsLayout scope={{ title: "Acme" }} items={items} current="implementer" onSelect={() => {}}>x</SettingsLayout>);
    expect(open).toContain('data-settings-nav="implementer"');
    expect(open).toMatch(/aria-current="page"[^>]*data-settings-nav="implementer"/);
    const closed = renderToStaticMarkup(<SettingsLayout scope={{ title: "Acme" }} items={items} current="delivery" onSelect={() => {}}>x</SettingsLayout>);
    expect(closed).not.toContain('data-settings-nav="implementer"');
  });
});

describe("EpicProgress", () => {
  test("every lane is a word too, not only a colour", () => {
    const t = text(renderToStaticMarkup(<EpicProgress lanes={{ done: 2, review: 1, progress: 0, backlog: 3 }} />));
    expect(t).toBe("2 done 1 in review 0 in progress 3 backlog");
  });
});
