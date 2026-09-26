/**
 * Server-rendered markup of the chat and sidebar components: what is shown,
 * what is a button, and what a screen reader is told. Interaction (clicks,
 * hover, layout) needs a DOM and is not covered here.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ChatComposer } from "../src/components/ChatComposer.tsx";
import { ChatMessage } from "../src/components/ChatMessage.tsx";
import { NavTreeRow } from "../src/components/NavTree.tsx";
import { QuestionCard } from "../src/components/QuestionCard.tsx";
import { AttentionList } from "../src/components/Sidebar.tsx";
import { ToolCallCard } from "../src/components/ToolCallCard.tsx";
import { Icon } from "../src/icons/index.tsx";
import { formatTimestamp } from "../src/util/format.ts";
import type { AttentionItem, NavProject, NavRow, NavTask } from "../src/util/navModel.ts";

const noop = () => {};
const html = (el: React.ReactElement) => renderToStaticMarkup(el);
/** Visible text of the `<button>` elements, in order. */
const buttons = (h: string) => [...h.matchAll(/<button\b([^>]*)>([\s\S]*?)<\/button>/g)].map((m) => ({ attrs: m[1]!, text: m[2]!.replace(/<[^>]+>/g, "") }));
// CSS module classes are empty strings under `bun test`, so these helpers
// read structure, attributes and text, never class names.
/** `aria-label`s of `role="group"` elements. */
const groups = (h: string) => [...h.matchAll(/<[a-z]+\b[^>]*role="group"[^>]*>/g)].map((m) => /aria-label="([^"]*)"/.exec(m[0])?.[1] ?? null);

describe("QuestionCard choices", () => {
  const options = ["Yes", "No"];

  test("waiting without onChoose: choices are left to the composer", () => {
    const h = html(<QuestionCard role="orchestrator" text="Ship it?" options={options} />);
    expect(h).not.toContain(">Yes<");
    expect(buttons(h)).toEqual([]);
  });

  test("waiting with onChoose: one numbered button per choice", () => {
    const h = html(<QuestionCard role="orchestrator" text="Ship it?" options={options} onChoose={noop} />);
    expect(buttons(h).map((b) => b.text)).toEqual(["1Yes", "2No"]);
  });

  test("answered and dismissed: choices are listed, not clickable", () => {
    for (const props of [{ answeredAt: "2026-09-24T10:05:00Z" }, { dismissed: true }]) {
      const h = html(<QuestionCard role="orchestrator" text="Ship it?" options={options} onChoose={noop} askedAt="2026-09-24T10:00:00Z" {...props} />);
      expect(buttons(h)).toEqual([]);
      expect(h).toContain("Yes</span>");
      expect(h).toContain('aria-label="Choices offered"');
    }
  });

  test("waiting on someone else: says who, lists the choices, offers none", () => {
    const h = html(<QuestionCard role="orchestrator" text="Ship it?" options={options} onChoose={noop} waitingOn="Ana" />);
    expect(buttons(h)).toEqual([]);
    expect(h).toContain("Waiting for Ana to answer</p>");
    expect(h).toContain('aria-label="Choices offered"');
    expect(h).not.toContain("Needs you");
  });

  test("waiting announces once in a status region", () => {
    const h = html(<QuestionCard role="orchestrator" text="Ship it?" />);
    expect(h).toMatch(/<span role="status"[^>]*>Needs you\. Blocked until you answer\.<\/span>/);
  });
});

describe("ToolCallCard exit chip", () => {
  /** The collapsed row is the `role="button"` header; the body follows it as a sibling. */
  const rowExit = (h: string) => {
    const start = h.indexOf('role="button"');
    const row = h.slice(start, h.indexOf("</div>", start));
    return /exit \d+/.exec(row.replace(/<[^>]+>/g, ""))?.[0] ?? null;
  };

  test.each([
    ["failed", 1, null],
    ["failed", 2, "exit 2"],
    ["completed", 1, "exit 1"],
    ["completed", 0, null],
  ] as const)("collapsed %s with exit %d shows %p on the row", (status, exitCode, expected) => {
    const h = html(<ToolCallCard name="bash" status={status} exitCode={exitCode} output="x" expanded={false} />);
    expect(rowExit(h)).toBe(expected);
  });

  test("expanded failed exit 1 still shows the code in the output header", () => {
    const h = html(<ToolCallCard name="bash" status="failed" exitCode={1} output="boom" expanded />);
    expect(rowExit(h)).toBeNull();
    expect(h).toMatch(/Output<span[^>]*title="Exited with code 1"[^>]*>exit 1<\/span>/);
  });
});

describe("ChatComposer answer mode", () => {
  const question = { id: "q1", text: "Ship it?", options: ["Yes", "No"] };

  test("names the asker, or the agent when unknown", () => {
    expect(html(<ChatComposer question={question} onSubmit={noop} />)).toContain("Answering the agent:");
    expect(html(<ChatComposer question={{ ...question, askedBy: "Orchestrator" }} onSubmit={noop} />)).toContain("Answering Orchestrator:");
  });

  test("choices are a labelled group of chips", () => {
    const h = html(<ChatComposer question={question} onSubmit={noop} />);
    expect(groups(h)).toEqual(["Answer with one of"]);
    const chips = buttons(h).filter((b) => b.attrs.includes('type="button"'));
    expect(chips.map((b) => b.text)).toEqual(["Yes", "No"]);
    expect(chips.every((b) => !/\bdisabled\b/.test(b.attrs))).toBe(true);
  });

  test("chips are disabled with the composer", () => {
    const h = html(<ChatComposer question={question} disabled onSubmit={noop} />);
    const chips = buttons(h).filter((b) => b.attrs.includes('type="button"'));
    expect(chips).toHaveLength(2);
    expect(chips.every((b) => /\bdisabled\b/.test(b.attrs))).toBe(true);
  });

  test("no group without options, and none in steer mode", () => {
    expect(groups(html(<ChatComposer question={{ id: "q", text: "?" }} onSubmit={noop} />))).toEqual([]);
    expect(groups(html(<ChatComposer question={{ id: "q", text: "?", options: [] }} onSubmit={noop} />))).toEqual([]);
    expect(groups(html(<ChatComposer mode="steer" question={question} onSubmit={noop} />))).toEqual([]);
  });
});

describe("NavTree who trailing", () => {
  const wi = (people: NavTask["people"], running: boolean): NavTask => ({
    id: "wi",
    key: "WI-1",
    title: "Retry",
    status: running ? "running" : "intake",
    people,
    runs: running
      ? [{ id: "r", attempt: 1, status: "running", sessions: [{ id: "s1", role: "implementer", status: "running" }, { id: "s2", role: "reviewer", status: "running" }] }]
      : [],
  });
  const row = (node: NavTask): NavRow => ({
    key: "wi",
    ref: { kind: "task", id: node.id },
    depth: 1,
    parentKey: null,
    expandable: false,
    expanded: false,
    forced: false,
    node,
    counts: null,
    triage: null,
    projectId: "p",
  });
  const render = (node: NavTask) =>
    html(<NavTreeRow row={row(node)} selected={false} tabIndex={0} onFocus={noop} onKeyDown={noop} onClick={noop} onToggle={noop} />);
  const more = (h: string) => />\+(\d+)<\/span>/.exec(h)?.[1] ?? null;

  test("people only: first person and +N, everyone in the label", () => {
    const h = render(wi([{ name: "Ann" }, { name: "Bo" }, { name: "Cy" }], false));
    expect(groups(h)).toEqual(["Ann, Bo, Cy"]);
    expect(more(h)).toBe("2");
    expect(h).toContain('aria-label="Ann"');
  });

  test("roles only: the first role's avatar", () => {
    const h = render(wi([], true));
    expect(groups(h)).toEqual(["Implementer, Reviewer"]);
    expect(h).toContain('aria-label="Implementer"');
    expect(more(h)).toBe("1");
  });

  test("people and roles: +N counts both", () => {
    const h = render(wi([{ name: "Ann" }], true));
    expect(groups(h)).toEqual(["Ann, Implementer, Reviewer"]);
    expect(more(h)).toBe("2");
  });

  test("nobody: nothing rendered", () => {
    const h = render(wi([], false));
    expect(groups(h)).toEqual([]);
    expect(more(h)).toBeNull();
  });
});

describe("AttentionList", () => {
  const project: NavProject = { id: "p", name: "Webhooks" };
  const item = (people: NavTask["people"], withSession: boolean): AttentionItem => ({
    task: { id: `wi-${people?.length ?? 0}-${withSession}`, key: "WI-9", title: "Retry", status: "awaiting_input", people },
    project,
    epic: { id: "e", title: "Reliability", tasks: [] },
    session: withSession ? { id: "s", role: "orchestrator", status: "awaiting_input", activity: "Which backoff?" } : null,
  });

  test("where is the row's title, not visible text", () => {
    const h = html(<AttentionList items={[item([], true)]} />);
    expect(h).toContain('title="Webhooks · Reliability"');
    expect(h.replace(/<[^>]+>/g, "|")).not.toContain("Webhooks · Reliability");
  });

  test("people are a named group with +N", () => {
    const h = html(<AttentionList items={[item([{ name: "Ann" }, { name: "Bo" }], true)]} />);
    expect(groups(h)).toEqual(["Ann, Bo"]);
    expect(h).toMatch(/>\+1<\/span>/);
  });

  test("without a session the asker slot is kept empty", () => {
    const h = html(<AttentionList items={[item([], false)]} />);
    expect(h).toContain("<span></span><span>waiting for you</span>");
  });
});

describe("ChatMessage gutter time and actions", () => {
  const at = new Date(2026, 8, 24, 9, 5, 7);

  test("a continued turn shows HH:MM in the gutter, hidden from screen readers", () => {
    const h = html(<ChatMessage role="implementer" content="more" startedAt={at} continued />);
    expect(h).toContain(`<div><time dateTime="${at.toISOString()}" aria-hidden="true">09:05</time></div>`);
  });

  test("a turn that starts a group has no gutter time", () => {
    const h = html(<ChatMessage role="implementer" content="first" startedAt={at} />);
    expect(h).not.toContain(">09:05</time>");
    expect(h).toContain(">09:05:07</time>");
  });

  test("actions render only when given", () => {
    expect(buttons(html(<ChatMessage role="implementer" content="x" />))).toEqual([]);
    const h = html(<ChatMessage role="implementer" content="x" actions={<button type="button">Copy</button>} />);
    expect(h).toContain('<div><button type="button">Copy</button></div>');
  });
});

describe("Icon stroke width", () => {
  const stroke = (size?: number | string) => /stroke-width="([\d.]+)"/.exec(html(<Icon name="check" size={size} />))?.[1];

  test("1.75 at 20px and up, 1.5 below and at the 1em default", () => {
    expect(stroke(20)).toBe("1.75");
    expect(stroke(24)).toBe("1.75");
    expect(stroke(19)).toBe("1.5");
    expect(stroke(14)).toBe("1.5");
    expect(stroke()).toBe("1.5");
    expect(stroke("1em")).toBe("1.5");
  });
});

describe("formatTimestamp time-short", () => {
  test("is HH:MM, zero-padded", () => {
    expect(formatTimestamp(new Date(2026, 8, 24, 9, 5, 7), "time-short")).toBe("09:05");
    expect(formatTimestamp(new Date(2026, 8, 24, 23, 59, 59), "time-short")).toBe("23:59");
  });
});

describe("page structure", () => {
  test("a header names the item, and a section its content", async () => {
    const { Page, PageHeader, Section, Callout, KeyValueList, Fieldset } = await import("../src/primitives/Layout.tsx");
    const html = renderToStaticMarkup(
      <Page>
        <PageHeader itemKey="TEXT-20" title="Add a helper" actions={<button>Move</button>} />
        <Section title="Pipeline" count={3}>rows</Section>
        <Callout tone="danger">refused</Callout>
        <Callout tone="info">noted</Callout>
        <KeyValueList items={[{ label: "Account", value: "me", mono: true }]} />
        <Fieldset legend="Reviewers" error="Choose one">x</Fieldset>
      </Page>,
    );
    expect(html).toContain("<h1");
    expect(html).toMatch(/<section[^>]*aria-labelledby="[^"]+"/);
    expect(html).toContain('role="alert"');
    expect(html).toContain('role="status"');
    expect(html).toContain("<dt");
    expect(html).toContain("<legend");
    expect(html).toContain("Choose one");
  });

  test("a step opens its conversation, or a page elsewhere", async () => {
    const { StepList, StepRow } = await import("../src/components/StepList.tsx");
    const html = renderToStaticMarkup(
      <StepList>
        <StepRow step="1" label="Implement" onOpen={() => undefined} />
        <StepRow step="PR" label="Pull request #15" href="https://github.com/x/pull/15" />
      </StepList>,
    );
    expect(html).toContain("<ol");
    expect(html).toContain('<button type="button"');
    expect(html).toContain('target="_blank"');
    expect(html).toContain('data-icon="external"');
  });
});
