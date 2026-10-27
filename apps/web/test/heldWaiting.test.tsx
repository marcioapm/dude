/**
 * A task Deliver holds before its pull request, on Waiting and the board:
 * the navigation tree's heldForReview is what it waits for in words, a
 * Waiting entry of kind Decisions "<KEY> is ready for your review", and an
 * In progress card with the needs-you mark and the same words.
 */

import { afterEach, expect, test } from "bun:test";
import { Board, type NavProject } from "@dude/design-system";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { SessionsList } from "@dude/domain";
import { READY_FOR_REVIEW_WAITING, withWaitingFor } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE, YOU } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { InboxScreen } from "../src/screens/InboxScreen.tsx";
import { mount, until } from "./dom.ts";

let cleanup: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of cleanup) await unmount();
  cleanup = [];
});

const me = PEOPLE.find((p) => p.id === YOU)!;
const tree = (): NavProject[] => withWaitingFor([{ id: "p_web", name: "Web", tasks: [
  { id: "t_142", key: "WEB-142", title: "Saved filters on the board", status: "awaiting_input", people: [me],
    statusSince: new Date(Date.now() - 22 * 60_000).toISOString(), heldForReview: true, decider: "policy", conductorLive: false },
] }] as unknown as NavProject[]);
const sessions: SessionsList = { sessions: [], questions: [], invitations: [] };

test("the tree's held task waits for its owner's review, with no pull request yet", () => {
  const [project] = tree();
  expect(project!.tasks![0]!.waitingFor).toBe(READY_FOR_REVIEW_WAITING);
  expect(READY_FOR_REVIEW_WAITING).toBe("Ready for your review: no pull request yet");
});

test("Waiting lists it as a decision: ready for your review, opened with Review", async () => {
  const client = new FixtureClient("a");
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider><PeopleProvider client={client}>
      <InboxScreen client={client} projects={tree()} sessions={sessions} tab="you" onTabChange={() => {}} onSelect={() => {}}
        onOpenSession={() => {}} onChanged={() => {}} />
    </PeopleProvider></ToastProvider></TooltipProvider>,
  );
  cleanup.push(unmount);
  const entry = await until(() => [...container.querySelectorAll<HTMLElement>("[data-testid=inbox] li")]
    .find((li) => li.textContent?.includes("WEB-142 is ready for your review")), "the entry");
  expect(entry.textContent).toContain("Decisions");
  expect(entry.textContent).toContain("no pull request yet");
  expect([...entry.querySelectorAll("button")].map((b) => b.textContent)).toContain("Review");
  const decisions = [...container.querySelectorAll("[role=checkbox]")].find((b) =>
    b.getAttribute("aria-labelledby")?.split(" ").some((id) => document.getElementById(id)?.textContent === "Decisions"));
  expect(decisions).toBeDefined();
});

test("the board keeps it In progress, needing you, with the hold's words", async () => {
  const { container, unmount } = await mount(<TooltipProvider><Board project={tree()[0]!} you={YOU} /></TooltipProvider>);
  cleanup.push(unmount);
  const card = await until(() => container.querySelector<HTMLElement>('[aria-label^="WEB-142"]'), "the card");
  const column = card.closest<HTMLElement>("section[data-column]")!;
  expect(column.getAttribute("data-column")).toBe("running");
  expect(document.getElementById(column.getAttribute("aria-labelledby")!)?.textContent).toContain("In progress");
  expect(card.textContent).toContain("Needs you");
  expect(card.textContent).toContain(READY_FOR_REVIEW_WAITING);
});
