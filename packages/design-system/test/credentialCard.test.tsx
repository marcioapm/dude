/**
 * CredentialCard and CredentialWarning: a pull request waiting on its
 * owner's GitHub, in the owner's words and everyone else's.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { CredentialCard, CredentialWarning, credentialWords } from "../src/components/CredentialCard.tsx";
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

const q = (el: HTMLElement, id: string) => el.querySelector<HTMLElement>(`[data-testid=${id}]`);
const told = new Date(Date.now() - 42 * 60_000).toISOString();

describe("CredentialCard", () => {
  test("the owner's: connect, the branch, Connect GitHub and Why as me?, no Take over", async () => {
    let connects = 0;
    const el = await mount(<CredentialCard person="Christophe Moreau" yours branch="dude/abs-38-1" askedAt={told}
      onConnect={() => connects++} onTakeOver={() => {}} />);
    expect(q(el, "credential-wait-title")!.textContent).toBe("Connect GitHub to open the pull request");
    expect(q(el, "credential-wait-label")!.textContent).toBe("Needs you · Connect GitHub");
    expect(el.textContent).toContain("dude opens pull requests as the task's owner, and you haven't connected your GitHub account.");
    expect(el.textContent).toContain("The branch dude/abs-38-1 is pushed and ready. Once you connect, the PR opens on its own. Nothing is redone.");
    expect(q(el, "credential-wait-take-over")).toBeNull();
    await act(async () => q(el, "credential-wait-connect")!.click());
    expect(connects).toBe(1);
    expect(q(el, "credential-wait-why")).toBeNull();
    await act(async () => q(el, "credential-wait-why-toggle")!.click());
    expect(q(el, "credential-wait-why")!.textContent).toContain("never as itself");
    // Meaning is not in the hue alone: a glyph and a label.
    expect(el.querySelector("svg")).not.toBeNull();
  });

  test("everyone else's: waiting for them, when they were told, and Take over only when offered", async () => {
    const el = await mount(<CredentialCard person="Christophe Moreau" yours={false} branch="dude/abs-38-1" askedAt={told} onTakeOver={() => {}} />);
    expect(q(el, "credential-wait-title")!.textContent).toBe("Waiting for Christophe Moreau to connect GitHub");
    expect(q(el, "credential-wait-label")!.textContent).toBe("Needs Christophe · Connect GitHub");
    expect(el.textContent).toContain("The pull request opens as its owner. Christophe Moreau was notified 42m ago.");
    expect(q(el, "credential-wait-take-over")!.textContent).toBe("Take over and open it as me");
    expect(q(el, "credential-wait-connect")).toBeNull();
    expect(q(el, "credential-wait-why-toggle")).toBeNull();
  });

  test("without onTakeOver nobody else is offered it", async () => {
    const el = await mount(<CredentialCard person="Christophe Moreau" yours={false} branch="b" askedAt={told} />);
    expect(q(el, "credential-wait-take-over")).toBeNull();
  });

  test("a connection that stopped working says reconnect", () => {
    expect(credentialWords("Ana Lima", true, true)).toEqual({ title: "Reconnect GitHub to open the pull request", label: "Needs you · Reconnect GitHub" });
    expect(credentialWords("Ana Lima", false, true).title).toBe("Waiting for Ana Lima to reconnect GitHub");
  });

  test("a wait on nobody known names the task's owner", async () => {
    expect(credentialWords(null, false)).toEqual({ title: "Waiting for the task's owner to connect GitHub", label: "Needs the owner · Connect GitHub" });
    expect(credentialWords("", false, true)).toEqual({ title: "Waiting for the task's owner to reconnect GitHub", label: "Needs the owner · Reconnect GitHub" });
    const el = await mount(<CredentialCard person={null} yours={false} branch="b" askedAt={told} />);
    expect(q(el, "credential-wait-label")!.textContent).toBe("Needs the owner · Connect GitHub");
    expect(el.textContent).toContain("The task's owner was notified 42m ago.");
  });
});

describe("CredentialWarning", () => {
  test("the owner gets Connect GitHub; anyone else is told to pick a connected owner or ask them", async () => {
    const mine = await mount(<CredentialWarning person="Christophe Moreau" yours onConnect={() => {}} />);
    expect(mine.textContent).toContain("Christophe Moreau hasn't connected GitHub. The task will do its work and then stop before its pull request.");
    expect(q(mine, "credential-warning-connect")).not.toBeNull();
    expect(q(mine, "credential-warning")!.getAttribute("data-tone")).toBe("attention");
    expect(mine.textContent).not.toContain("Pick an owner");
    await act(async () => root?.unmount());
    host?.remove();
    const theirs = await mount(<CredentialWarning person="Christophe Moreau" yours={false} />);
    expect(q(theirs, "credential-warning-connect")).toBeNull();
    expect(theirs.textContent).toContain("before its pull request. Pick an owner who has connected GitHub, or ask Christophe to connect.");
    // Plain body text in the Callout's own ink: no muted span.
    expect(q(theirs, "credential-warning")!.querySelector("span")).toBeNull();
  });
});
