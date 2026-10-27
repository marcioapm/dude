/**
 * The organisation's secrets in the fixtures (migration 210): names,
 * recipients and hints, never a value. Which projects have their own is
 * read from the project's secrets as they are now, and "Picked by" from the
 * automations; writes change the next read, as a backend would.
 */

import type { Automation, OrgSecret, OrgSecretListing, OrgSecretsResponse, SecretRecipient } from "@dude/domain";
import { ApiError } from "../api/client.ts";
import { orgSecretNameProblem, secretHint } from "@dude/domain";
import { PEOPLE, PROJECT, YOU } from "./data.ts";

const DAY = 864e5;
const at = (ago: number) => new Date(Date.now() - ago).toISOString();
const by = (id: string) => ({ id, name: PEOPLE.find((p) => p.id === id)?.name ?? PEOPLE[0]!.name });

/** Another project's own names: platform holds a GH_TOKEN of its own. */
const ELSEWHERE: Record<string, { id: string; name: string }[]> = { GH_TOKEN: [{ id: "p_platform", name: "platform" }] };

export const ORG_SECRETS: OrgSecret[] = [
  { name: "GH_TOKEN", recipients: ["implementer", "fixer"], hint: "Q8e2", updatedAt: at(6 * DAY), updatedBy: by("u_marcio") },
  { name: "NPM_TOKEN", recipients: ["previews", "tester", "implementer", "fixer", "simplifier", "reviewer"], hint: "x0Lm", updatedAt: at(20 * DAY), updatedBy: by("u_ana") },
  { name: "SEED_LLM_KEY", recipients: ["tester"], hint: "Kq7w", updatedAt: at(9 * DAY), updatedBy: by("u_marcio") },
  { name: "SENTRY_TOKEN", recipients: ["tester", "implementer"], hint: "7c1d", updatedAt: at(3 * DAY), updatedBy: by("u_marcio") },
  { name: "STATUSPAGE_KEY", recipients: [], hint: "kk42", updatedAt: at(DAY), updatedBy: by("u_marcio") },
];

export class FixtureOrgSecrets {
  list: OrgSecret[] = ORG_SECRETS.map((s) => ({ ...s, recipients: [...s.recipients] }));

  read(org: { id: string; name: string }, projectNames: readonly string[], automations: readonly Automation[]): OrgSecretsResponse {
    const listing = (s: OrgSecret): OrgSecretListing => ({
      ...s,
      overriddenIn: [...(ELSEWHERE[s.name] ?? []), ...(projectNames.includes(s.name) ? [{ id: PROJECT.id, name: PROJECT.name }] : [])]
        .sort((a, b) => a.name.localeCompare(b.name)),
      pickedBy: automations.filter((a) => a.secrets.some((r) => r.source === "organization" && r.name === s.name))
        .map((a) => ({ id: a.id, name: a.name, projectId: a.projectId })),
    });
    return { organization: org, secrets: this.list.map(listing) };
  }

  #at(name: string): number {
    const i = this.list.findIndex((s) => s.name === name);
    if (i < 0) throw new ApiError(404, "not_found", `the organisation has no secret ${name}`);
    return i;
  }

  add(name: string, value: string, recipients: readonly SecretRecipient[]): OrgSecret {
    const problem = orgSecretNameProblem(name, this.list.map((s) => s.name));
    if (problem) throw new ApiError(problem.kind === "conflict" ? 409 : 400, problem.kind, problem.message);
    const saved: OrgSecret = { name, recipients: [...recipients], hint: secretHint(value), updatedAt: new Date().toISOString(), updatedBy: by(YOU) };
    this.list = [...this.list, saved].sort((a, b) => a.name.localeCompare(b.name));
    return saved;
  }

  recipients(name: string, recipients: readonly SecretRecipient[]): OrgSecret {
    const i = this.#at(name);
    const saved = { ...this.list[i]!, recipients: [...recipients], updatedAt: new Date().toISOString(), updatedBy: by(YOU) };
    this.list[i] = saved;
    return saved;
  }

  replace(name: string, value: string): OrgSecret {
    const i = this.#at(name);
    const saved = { ...this.list[i]!, hint: secretHint(value), updatedAt: new Date().toISOString(), updatedBy: by(YOU) };
    this.list[i] = saved;
    return saved;
  }

  remove(name: string): void {
    this.#at(name);
    this.list = this.list.filter((s) => s.name !== name);
  }
}
