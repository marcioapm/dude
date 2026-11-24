/**
 * A pull request that waits on its owner's GitHub connection (#513), as
 * the task's screens show it: the card where the delivery parked, the
 * header's chip, and the warning before work starts. Connect GitHub from
 * here comes back to the page it was started on.
 */

import { useState } from "react";
import { CredentialCard, CredentialWarning, credentialWords } from "@dude/design-system/components";
import { useToast } from "@dude/design-system/primitives";
import type { ApiClient, CredentialWait } from "./api/client.ts";
import { errorText } from "./hooks/useSave.tsx";
import type { People } from "./people.tsx";

/** Where Connect GitHub returns to once its callback lands on your settings. */
const RETURN_KEY = "dude.github.return";

/** Starts Connect GitHub, to come back to the page it was started on. */
export async function connectGithubHere(client: ApiClient): Promise<void> {
  const { url } = await client.connectGithub();
  try {
    sessionStorage.setItem(RETURN_KEY, window.location.hash);
  } catch {
    // Without storage the callback lands on your settings, as it always has.
  }
  window.location.assign(url);
}

/**
 * After a callback that connected: the page Connect GitHub was started on,
 * once; null for none (it was started from your settings).
 */
export function takeGithubReturn(outcome: string | null): string | null {
  try {
    const to = sessionStorage.getItem(RETURN_KEY);
    sessionStorage.removeItem(RETURN_KEY);
    return outcome === "connected" && to && !to.includes("/me/settings") ? to : null;
  } catch {
    return null;
  }
}

/** The header chip's words while the pull request waits on a person's GitHub. */
export function credentialLabel(wait: CredentialWait, you: string | null): string {
  const yours = wait.person !== null && wait.person.id === you;
  return credentialWords(wait.person?.name ?? null, yours, wait.reconnect).label;
}

/**
 * The reader may take the task over and open it as themselves: they have a
 * live GitHub connection, and it is not already theirs.
 */
export function mayTakeOverAndOpen(wait: CredentialWait, people: Pick<People, "you" | "me">): boolean {
  return people.you !== null && wait.person?.id !== people.you && people.me?.githubConnected === true;
}

/** The card, where the delivery parked: the owner's to connect, a plain fact for anyone else. */
export function CredentialWaitPanel({ client, taskId, wait, people, onChanged }: {
  client: ApiClient; taskId: string; wait: CredentialWait; people: Pick<People, "you" | "me">; onChanged: () => void;
}) {
  const { toast } = useToast();
  const [busy, setBusy] = useState<"connect" | "take" | null>(null);
  const yours = wait.person !== null && wait.person.id === people.you;
  const act = async (what: "connect" | "take", fn: () => Promise<unknown>) => {
    setBusy(what);
    try {
      await fn();
      if (what === "take") onChanged();
    } catch (err) {
      toast({ title: `${what === "connect" ? "Could not start Connect GitHub" : "Could not take it over"}: ${errorText(err)}`, tone: "danger" });
    } finally {
      setBusy(null);
    }
  };
  return (
    <CredentialCard person={wait.person?.name ?? null} yours={yours} branch={wait.branch} reconnect={wait.reconnect} askedAt={wait.askedAt}
      connecting={busy === "connect"} takingOver={busy === "take"}
      onConnect={yours ? () => void act("connect", () => connectGithubHere(client)) : undefined}
      onTakeOver={mayTakeOverAndOpen(wait, people) ? () => void act("take", () => client.reassignTask(taskId, people.you!)) : undefined} />
  );
}

/**
 * Before the pull request: the owner has no live GitHub connection, so the
 * task would stop before it. Nothing when the organisation opens pull
 * requests otherwise (githubConnected null) or nobody owns it.
 */
export function OwnerGithubWarning({ client, owner, people }: {
  client: ApiClient; owner: { id: string; name: string } | null; people: Pick<People, "you" | "byId">;
}) {
  const known = owner ? people.byId.get(owner.id) : undefined;
  if (!owner || known?.githubConnected !== false) return null;
  const yours = owner.id === people.you;
  return <CredentialWarning person={owner.name} yours={yours} onConnect={yours ? () => void connectGithubHere(client) : undefined} />;
}
