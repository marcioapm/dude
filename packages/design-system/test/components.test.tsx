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
import { Sidebar, SidebarToggle } from "../src/components/Sidebar.tsx";
import { ChatTranscript } from "../src/components/ChatTranscript.tsx";
import { FindingRow } from "../src/components/FindingRow.tsx";
import { EventRow } from "../src/components/EventRow.tsx";
import { ToolCallCard } from "../src/components/ToolCallCard.tsx";
import { Icon } from "../src/icons/index.tsx";
import { formatTimestamp } from "../src/util/format.ts";
import type { NavProject, NavRow, NavTask } from "../src/util/navModel.ts";

const noop = () => {};
const html = (el: React.ReactElement) => renderToStaticMarkup(el);
/** Visible text of the `<button>` elements, in order. */
const buttons = (h: string) => [...h.matchAll(/<button\b([^>]*)>([\s\S]*?)<\/button>/g)].map((m) => ({ attrs: m[1]!, text: m[2]!.replace(/<[^>]+>/g, "") }));
// CSS module classes are empty strings under `bun test`, so these helpers
// read structure, attributes and text, never class names.
/** `aria-label`s of `role="group"` elements. */
const groups = (h: string) => [...h.matchAll(/<[a-z]+\b[^>]*role="group"[^>]*>/g)].map((m) => /aria-label="([^"]*)"/.exec(m[0])?.[1] ?? null);

/** The text inside the first element carrying `attr`, tags stripped. */
function text(html: string, attr: string): string {
  const from = html.indexOf(attr);
  const open = html.lastIndexOf("<", from);
  const tag = html.slice(open + 1, html.indexOf(" ", open));
  const end = html.indexOf(`</${tag}>`, from);
  return html.slice(html.indexOf(">", from) + 1, end).replace(/<[^>]+>/g, "");
}

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
    // Whom it waits on, and how to make it yours, in words everyone sees.
    expect(text(h, 'data-testid="waiting-on"')).toBe("Waiting for Ana to answer · Take over this task to answer");
    expect(h).not.toContain("tabindex");
    expect(h).toContain('aria-label="Choices offered"');
    expect(h).not.toContain("Needs you");
  });

  test("a question with no choices still says how to make it yours", () => {
    expect(html(<QuestionCard role="orchestrator" text="Which locale?" onChoose={noop} waitingOn="Ana" />)).toContain("Take over this task to answer");
  });

  test("a request waiting on someone else is theirs to decide", () => {
    const h = html(<QuestionCard role="orchestrator" text="Read web?" options={["Approve", "Decline"]} onChoose={noop} waitingOn="Ana" kind="request" />);
    expect(text(h, 'data-testid="waiting-on"')).toBe("Waiting for Ana to decide · Take over this task to decide");
    expect(h).toContain("asks for a repository and is waiting for Ana to decide");
    expect(h).toContain("Blocked until Ana decides.");
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

describe("NavTree task row: its people and its one state", () => {
  const wi = (people: NavTask["people"], running: boolean, extra: Partial<NavTask> = {}): NavTask => ({
    id: "wi",
    key: "WI-1",
    title: "Retry",
    status: running ? "running" : "intake",
    people,
    runs: running
      ? [{ id: "r", attempt: 1, status: "running", sessions: [{ id: "s1", role: "implementer", status: "running" }, { id: "s2", role: "reviewer", status: "completed" }] }]
      : [],
    ...extra,
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

  test("everyone on it is a face, named in the group", () => {
    const h = render(wi([{ id: "a", name: "Ann" }, { id: "b", name: "Bo" }], false));
    expect(groups(h)).toEqual(["Ann, Bo"]);
    expect(h).toContain('aria-label="Ann"');
    expect(h).toContain('aria-label="Bo"');
  });

  test("the agent working now sits on the owner's face", () => {
    const h = render(wi([{ id: "a", name: "Ann" }, { id: "b", name: "Bo" }], true));
    expect(h).toContain('aria-label="Ann · Implementer working for them now"');
    expect(h).toContain('data-role="implementer"');
    expect(h).not.toContain('data-role="reviewer"');
  });

  test("nobody: no faces", () => {
    expect(groups(render(wi([], true)))).toEqual([]);
  });

  test("a pull request shows its one state, not the task's", () => {
    const pr = { number: 41, url: "https://github.com/o/r/pull/41", state: "open", checks: "failing", review: "approved" } as const;
    const h = render(wi([], false, { status: "review", pullRequests: [pr] }));
    expect(h).toContain('data-pr-state="ci_red"');
    expect(h).not.toContain('data-status="review"');
  });
});

describe("the tree under a task: only what works now", () => {
  test("finished phases are not rows; the running one is", async () => {
    const { flattenNav } = await import("../src/util/navModel.ts");
    const project: NavProject = {
      id: "p",
      name: "p",
      tasks: [
        {
          id: "t",
          title: "t",
          status: "running",
          runs: [{ id: "r", attempt: 1, status: "running", sessions: [
            { id: "impl", role: "implementer", status: "completed" },
            { id: "rev", role: "reviewer", status: "completed" },
            { id: "fix", role: "implementer", status: "running", activity: "fixing" },
          ] }],
        },
      ],
    };
    expect(flattenNav([project], new Map()).map((r) => r.key)).toEqual(["project:p", "task:t", "session:fix"]);
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

describe("ChatTranscript header", () => {
  const session = { id: "run_0mugpn7v3", role: "implementer", status: "completed", taskId: "wi_0mugpn7ul5", taskKey: "TEXT-14", title: "Title-case" } as const;
  test("shows the task key; raw ids only in tooltips", () => {
    const h = html(<ChatTranscript session={session} />);
    expect(h).toContain(">TEXT-14<");
    expect(h).not.toContain(">wi_0mugpn7ul5<");
    expect(h).not.toContain(">run_0mugpn7v3<");
    expect(h).toContain('title="task wi_0mugpn7ul5 · session run_0mugpn7v3"');
  });
  test("no key: no raw id in its place", () => {
    const h = html(<ChatTranscript session={{ ...session, taskKey: undefined }} />);
    expect(h).not.toContain(">wi_0mugpn7ul5<");
  });
});

describe("FindingRow", () => {
  test("the category keeps its full name in a tooltip", () => {
    const h = html(<FindingRow severity="low" status="open" category="input-validation" title="t" />);
    expect(h).toContain('title="input-validation"');
  });
  test("fixedIn reads \"fixed in Fix 2\", with no arrow drawn by the row", () => {
    const h = html(<FindingRow severity="low" status="resolved" category="c" title="t" fixedIn={<a href="#r">Fix 2</a>} />);
    const text = h.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");
    expect(text).toContain("fixed in Fix 2");
    expect(h).not.toContain('data-icon="chevron-right"');
  });
});

describe("EventRow", () => {
  test("the full event type is in the title", () => {
    const h = html(<EventRow occurredAt={0} actor={{ type: "agent", role: "implementer" }} eventType="agent.model.request.completed" summary="s" />);
    expect(h).toContain('title="agent.model.request.completed"');
  });
});

describe("Sidebar drawer", () => {
  test("not collapsible: no scrim, no open state", () => {
    const h = html(<Sidebar projects={[]} />);
    expect(h).not.toContain("data-open");
  });
  test("collapsible: says whether the drawer is open", () => {
    expect(html(<Sidebar projects={[]} collapsible open onOpenChange={noop} />)).toContain('data-open="true"');
    expect(html(<Sidebar projects={[]} collapsible open={false} onOpenChange={noop} />)).toContain('data-open="false"');
  });
  test("the toggle is a labelled button that reports its state", () => {
    const h = html(<SidebarToggle open={false} onOpenChange={noop} controls="nav" />);
    expect(h).toContain('aria-label="Navigation"');
    expect(h).toContain('aria-expanded="false"');
    expect(h).toContain('aria-controls="nav"');
  });
});
