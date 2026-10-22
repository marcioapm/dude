/**
 * Stopping a task's parts: LiveWork and its sentence, DeciderField, and a
 * Chat's AttemptDivider over the earlier attempt's turns.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { AttemptDivider, EarlierTurns } from "../src/components/AttemptDivider.tsx";
import { ChatRunLine } from "../src/components/ChatRunLine.tsx";
import { QuestionCard } from "../src/components/QuestionCard.tsx";
import { DeciderField, type NextDecider } from "../src/components/DeciderField.tsx";
import { LiveWork, liveWords, type LiveItem } from "../src/components/LiveWork.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount(node: ReactNode): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{node}</TooltipProvider>));
  return host;
}

const team: LiveItem[] = [
  { id: "r1", role: "reviewer", who: "Reviewer", doing: "security" },
  { id: "c", role: "conductor", who: "Conductor", doing: "deciding after review" },
  { id: "r2", role: "reviewer", who: "Reviewer", doing: "correctness" },
  { id: "f", role: "implementer", who: "Fixer", doing: "the two findings", state: "queued" },
];

describe("liveWords", () => {
  test("names the conductor first, counts alike, and says a queued one", () => {
    expect(liveWords(team)).toBe("the conductor, 2 reviewers and a queued fixer");
    expect(liveWords([team[1]!])).toBe("the conductor");
    expect(liveWords([team[0]!, team[1]!])).toBe("the conductor and a reviewer");
    expect(liveWords([{ id: "i", role: "implementer", who: "Implementer", doing: "" }])).toBe("an implementer");
    expect(liveWords([])).toBe("nothing");
  });
});

describe("LiveWork", () => {
  test("one sentence, then a face per Run in a list named Live now; the ring only on those at work", async () => {
    const el = await mount(<LiveWork summary="The conductor and 2 reviewers are working." items={team} />);
    expect(el.querySelector("p")!.textContent).toBe("The conductor and 2 reviewers are working.");
    const list = el.querySelector('[aria-label="Live now"]')!;
    expect(list.tagName).toBe("UL");
    const rows = [...list.querySelectorAll("li")];
    expect(rows.map((r) => r.textContent)).toEqual(["Reviewersecurity", "Conductordeciding after review", "Reviewercorrectness", "Fixerthe two findings"]);
    const ringed = rows.map((r) => r.querySelector('[role="img"]')!.children.length > 1);
    expect(ringed).toEqual([true, true, true, false]);
    expect(rows.map((r) => r.getAttribute("data-state"))).toEqual(["working", "working", "working", "queued"]);
  });
});

describe("DeciderField", () => {
  test("a labelled group of two with their glyphs; as now on the previous one; the hint describes it", async () => {
    const changes: NextDecider[] = [];
    const el = await mount(<DeciderField attempt={2} value="conductor" previous="conductor" onChange={(v) => changes.push(v)} />);
    const group = el.querySelector('[role="group"]')!;
    expect(group.getAttribute("aria-label")).toBe("Who decides attempt 2");
    const hint = document.getElementById(group.getAttribute("aria-describedby")!)!;
    expect(hint.textContent).toBe("A new conductor, briefed with your note and what attempt 1 did. Each finished step comes back to it, in Chat, with you.");
    const buttons = [...group.querySelectorAll("button")];
    expect(buttons.map((b) => b.textContent)).toEqual(["The conductoras now", "Deliver"]);
    expect(buttons[0]!.querySelector('svg[data-icon="conductor"]')).not.toBeNull();
    expect(buttons[1]!.querySelector('svg[data-icon="zap"]')).not.toBeNull();
    expect(buttons[0]!.getAttribute("aria-pressed")).toBe("true");
    await act(async () => buttons[1]!.click());
    expect(changes).toEqual(["deliver"]);
  });

  test("a Deliver attempt marks Deliver as now and says what Deliver does", async () => {
    const el = await mount(<DeciderField attempt={3} value="deliver" previous="deliver" onChange={() => undefined} />);
    expect([...el.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["The conductor", "Deliveras now"]);
    expect(el.textContent).toContain("Deliver runs attempt 3 to the pull request on its own");
  });
});

describe("AttemptDivider and EarlierTurns", () => {
  test("separators on the rules named for the attempt and who started it; the words and note plain text", async () => {
    const at = Date.now() - 60_000;
    const el = await mount(<>
      <AttemptDivider attempt={2} by="Ana" note="Start from the API." at={at} />
      <AttemptDivider attempt={3} by="Bo" at={at} />
    </>);
    const [two, three] = [...el.querySelectorAll<HTMLElement>("[data-testid=attempt-divider]")];
    const rules = [...two!.querySelectorAll("hr")];
    expect(rules.map((r) => r.getAttribute("aria-label"))).toEqual(["Attempt 2, started by Ana", null]);
    expect(rules[1]!.getAttribute("aria-hidden")).toBe("true");
    // The words and the note are not inside any separator, so nothing flattens them away.
    expect(two!.querySelector("[role=separator]")).toBeNull();
    expect(two!.textContent).toContain("Attempt 2 · started by Ana · 1m ago");
    expect(two!.querySelector("q")!.textContent).toBe("Start from the API.");
    expect(three!.querySelector("q")).toBeNull();
  });

  test("earlier turns: a named group whose text is reachable, and nothing in it acts", async () => {
    let opened = 0;
    let chosen = 0;
    const line = (key: string) => <ChatRunLine key={key} role="implementer" status="completed" what="implement" onOpen={() => opened++} />;
    const el = await mount(<>
      <EarlierTurns attempt={1}>
        <p>Earlier turn</p>
        {line("a")}
        <QuestionCard role="conductor" text="Split it?" options={["Yes", "No"]} askedAt={0} onChoose={() => chosen++} onSubmit={() => undefined} />
      </EarlierTurns>
      {line("b")}
    </>);
    const group = el.querySelector<HTMLElement>('[role="group"][aria-label="Attempt 1\'s conversation"]')!;
    // Nothing hides it from assistive tech or selection.
    expect(group.hasAttribute("inert")).toBe(false);
    expect(group.closest("[aria-hidden=true], [inert]")).toBeNull();
    expect(group.textContent).toContain("Earlier turn");
    expect(group.querySelectorAll("button, [role=radio], textarea, input")).toHaveLength(0);
    // The same line outside the group still opens its session.
    const live = [...el.querySelectorAll<HTMLButtonElement>("button")].filter((b) => !group.contains(b));
    expect(live).toHaveLength(1);
    await act(async () => live[0]!.click());
    expect([opened, chosen]).toEqual([1, 0]);
  });
});
