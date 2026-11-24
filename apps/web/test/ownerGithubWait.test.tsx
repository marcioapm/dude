/**
 * A pull request waiting on its owner's GitHub (#513): the card in the
 * owner's words and everyone else's, Take over only for a reader who has
 * connected, the header's chip, the board's words, and the warning before
 * work starts.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { NavProject } from "@dude/design-system";
import { withWaitingFor, type CredentialWait } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE } from "../src/fixtures/data.ts";
import { CredentialWaitPanel, OwnerGithubWarning, credentialLabel, mayTakeOverAndOpen } from "../src/ownerGitHub.tsx";
import type { Someone } from "../src/people.tsx";
import { mount } from "./dom.ts";

let cleanup: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of cleanup) await unmount();
  cleanup = [];
  document.body.innerHTML = "";
});

const ana = PEOPLE.find((p) => p.id === "u_ana")!;
const tom = PEOPLE.find((p) => p.id === "u_tom")!;
const wait: CredentialWait = { reason: "owner_github", person: ana, branch: "dude/web-142-1", reconnect: false,
  askedAt: new Date(Date.now() - 42 * 60_000).toISOString() };
const as = (me: Someone, githubConnected: boolean | null) => ({ you: me.id, me: { ...me, githubConnected } });

async function panel(people: ReturnType<typeof as>, client = new FixtureClient("a")) {
  const { container, unmount } = await mount(<TooltipProvider><ToastProvider>
    <CredentialWaitPanel client={client} taskId="t_142" wait={wait} people={people} onChanged={() => {}} />
  </ToastProvider></TooltipProvider>);
  cleanup.push(unmount);
  return container;
}

test("the owner reads Connect GitHub with the branch, and is not offered Take over", async () => {
  const el = await panel(as(ana, false));
  expect(el.querySelector("[data-testid=credential-wait-title]")!.textContent).toBe("Connect GitHub to open the pull request");
  expect(el.textContent).toContain("dude/web-142-1");
  expect(el.querySelector("[data-testid=credential-wait-connect]")).not.toBeNull();
  expect(el.querySelector("[data-testid=credential-wait-take-over]")).toBeNull();
});

test("someone else who has connected may take it over and open it as them", async () => {
  const el = await panel(as(tom, true));
  expect(el.querySelector("[data-testid=credential-wait-title]")!.textContent).toBe("Waiting for Ana Ribeiro to connect GitHub");
  expect(el.textContent).toContain("Ana Ribeiro was notified 42m ago");
  expect(el.querySelector("[data-testid=credential-wait-connect]")).toBeNull();
  expect(el.querySelector("[data-testid=credential-wait-take-over]")).not.toBeNull();
});

test("someone else with no GitHub connection is not offered Take over", async () => {
  for (const connected of [false, null]) {
    const el = await panel(as(tom, connected));
    expect(el.querySelector("[data-testid=credential-wait-take-over]")).toBeNull();
    expect(mayTakeOverAndOpen(wait, as(tom, connected))).toBe(false);
  }
  expect(mayTakeOverAndOpen(wait, as(tom, true))).toBe(true);
  expect(mayTakeOverAndOpen(wait, as(ana, true))).toBe(false);
});

test("the header chip: Needs you for the owner, Needs <first name> for everyone else", () => {
  expect(credentialLabel(wait, ana.id)).toBe("Needs you · Connect GitHub");
  expect(credentialLabel(wait, tom.id)).toBe("Needs Ana · Connect GitHub");
  expect(credentialLabel({ ...wait, reconnect: true }, ana.id)).toBe("Needs you · Reconnect GitHub");
});

test("a wait whose person is not known names the task's owner, never a bare article", async () => {
  const nobody = { ...wait, person: null };
  expect(credentialLabel(nobody, tom.id)).toBe("Needs the owner · Connect GitHub");
  const { container, unmount } = await mount(<TooltipProvider><ToastProvider>
    <CredentialWaitPanel client={new FixtureClient("a")} taskId="t_142" wait={nobody} people={as(tom, true)} onChanged={() => {}} />
  </ToastProvider></TooltipProvider>);
  cleanup.push(unmount);
  expect(container.querySelector("[data-testid=credential-wait-title]")!.textContent).toBe("Waiting for the task's owner to connect GitHub");
});

test("on the board and in the tree it waits for Connect GitHub", () => {
  const projects = withWaitingFor([{ id: "p", name: "Web", tasks: [{ id: "t", key: "WEB-1", title: "T", status: "awaiting_input",
    people: [ana], escalation: null, credentialWait: { person: ana, reconnect: false } }] }] as unknown as NavProject[]);
  expect(projects[0]!.tasks![0]!.waitingFor).toBe("Connect GitHub to open the pull request");
});

test("the warning before work starts: only for an owner known not to have connected", async () => {
  const byId = (p: Someone, githubConnected: boolean | null | undefined) =>
    new Map<string, Someone>([[p.id, githubConnected === undefined ? p : { ...p, githubConnected }]]);
  const draw = async (people: Parameters<typeof OwnerGithubWarning>[0]["people"]) => {
    const { container, unmount } = await mount(<TooltipProvider><OwnerGithubWarning client={new FixtureClient("a")} owner={ana} people={people} /></TooltipProvider>);
    cleanup.push(unmount);
    return container;
  };
  const mine = await draw({ you: ana.id, byId: byId(ana, false) });
  expect(mine.textContent).toContain("Ana Ribeiro hasn't connected GitHub. The task will do its work and then stop before its pull request.");
  expect(mine.querySelector("[data-testid=credential-warning-connect]")).not.toBeNull();
  const theirs = await draw({ you: tom.id, byId: byId(ana, false) });
  expect(theirs.querySelector("[data-testid=credential-warning-connect]")).toBeNull();
  expect(theirs.textContent).toContain("Pick an owner who has connected GitHub, or ask Ana to connect.");
  for (const known of [true, null, undefined]) {
    const none = await draw({ you: tom.id, byId: byId(ana, known) });
    expect(none.querySelector("[data-testid=credential-warning]")).toBeNull();
  }
});
