/**
 * Organisation settings → Secrets: values every project's agents and
 * previews may need, entered once. Each reaches the agents ticked for it in
 * every project; a project secret of the same name takes its place there.
 * The project page's grammar one level up, with "Picked by" (the
 * automations that pick each one, for admins) and the projects that have
 * their own.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { GivenTo, SettingsHeader, SettingsNote, SettingsSection } from "@dude/design-system/components";
import { Button, Callout, EmptyState, FormActions, RowMenu, Spinner, Table, TBody, Td, Th, THead, Tooltip, Tr } from "@dude/design-system/primitives";
import { ORG_SECRET_NOBODY, secretRecipientLabel, type OrgSecretListing } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { useAccount } from "../accounts/context.tsx";
import { errorText } from "../hooks/useSave.tsx";
import {
  AddSecretDialog, ChangeValueDialog, NO_TALKERS_NOTE, RecipientsDialog, REDACTION_NOTE, RemoveSecretDialog, SECRETS_NOTE,
  changedWords, recipientsDone, useSecretDialogs, type SecretLevel,
} from "./ProjectSecrets.tsx";
import "../secrets.css";

export const ORG_SECRETS_HELP = (org: string) =>
  `Values every project's agents and previews may need, entered once for ${org}. Each reaches only the agents you tick, in every project, as an environment variable. A project secret of the same name takes its place in that project.`;
export const ORG_SECRETS_SAME_NAME = (org: string) =>
  `A project secret with the same name takes the place of ${org}'s in that project: its agents, previews and automations get the project's value.`;

/**
 * The organisation this screen is in: the account's current one, the
 * client's, or, with an API key naming none, the one `/v1/me` answers.
 */
export function useOrgId(client: ApiClient): string | undefined {
  const account = useAccount();
  const known = account?.current.id ?? client.organizationId;
  const [asked, setAsked] = useState<string | undefined>(undefined);
  useEffect(() => {
    if (known) return;
    let live = true;
    client.me().then((m) => { if (live) setAsked(m.organization.id); }, () => undefined);
    return () => { live = false; };
  }, [client, known]);
  return known ?? asked;
}

/** The organisation's secrets, read once for a screen; reload after a change. */
export function useOrgSecrets(client: ApiClient, given?: string | undefined) {
  const found = useOrgId(client);
  const orgId = given ?? found;
  const [secrets, setSecrets] = useState<OrgSecretListing[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const latest = useRef(0);
  const reload = useCallback(() => {
    if (!orgId) return;
    const mine = ++latest.current;
    client.organizationSecrets(orgId).then((r) => {
      if (mine === latest.current) { setSecrets(r.secrets); setProblem(null); }
    }, (e: unknown) => { if (mine === latest.current) setProblem(errorText(e)); });
  }, [client, orgId]);
  useEffect(reload, [reload]);
  return { secrets, problem, reload, orgId };
}

const projectsHaveOwn = (names: readonly string[]) => `${names.join(", ")} ${names.length === 1 ? "has" : "have"} its own`;

export function OrgSecretsPage({ client, orgName, admin }: { client: ApiClient; orgName: string; admin: boolean }) {
  const { secrets, problem, reload, orgId } = useOrgSecrets(client);
  const addButton = useRef<HTMLButtonElement>(null);
  const { editing, open, close, save, busy, problem: saveProblem } = useSecretDialogs(reload, () => addButton.current?.focus());
  const level: SecretLevel = { kind: "organization", orgName };
  if (!secrets) return <div className="centered">{problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />}</div>;
  const addAction = admin ? (
    <Button ref={addButton} variant="secondary" leadingIcon="plus" onClick={() => open({ kind: "add" })} data-testid="add-secret">Add secret</Button>
  ) : undefined;
  const overriddenIn = (s: OrgSecretListing) => s.overriddenIn.map((p) => p.name);
  const id = orgId!;

  return (
    <div className="settingsPage" data-testid="org-secrets">
      <SettingsHeader title="Secrets" description={ORG_SECRETS_HELP(orgName)} />
      <SettingsSection title={`${orgName}'s secrets`} id="secrets">
        {admin ? null : <SettingsNote icon="info">Only {orgName}&rsquo;s admins change these.</SettingsNote>}
        {secrets.length === 0 ? (
          <div data-testid="secrets-empty">
            <EmptyState compact icon="braces" title="No secrets"
              description="Add a value every project's agents need: a CLI's token, a package registry key." action={addAction} />
          </div>
        ) : (
          <>
            <Table density="compact" aria-label={`${orgName}'s secrets`} data-testid="secrets">
              <THead>
                <Tr>
                  <Th width="32%">Name</Th>
                  <Th>Given to</Th>
                  {admin ? <Th hideWhenNarrow width="18%">Picked by</Th> : null}
                  <Th hideWhenNarrow width={88}>Value</Th>
                  {admin ? <Th align="right" width="48px"><span className="ds-sr-only">Actions</span></Th> : null}
                </Tr>
              </THead>
              <TBody>
                {secrets.map((s) => (
                  <Tooltip key={s.name} side="bottom" content={changedWords(s)}>
                    <Tr data-secret={s.name}>
                      <Td wrap>
                        <span className="secretName">
                          <span className="ds-mono">{s.name}</span>
                          {s.overriddenIn.length ? <span className="secretAside" data-testid="has-own">{projectsHaveOwn(overriddenIn(s))}</span> : null}
                        </span>
                      </Td>
                      <Td wrap><GivenTo given={s.recipients.map(secretRecipientLabel)} none={ORG_SECRET_NOBODY} /></Td>
                      {admin ? (
                        <Td hideWhenNarrow muted wrap>
                          {s.pickedBy?.length ? (
                            <Tooltip side="bottom" content={s.pickedBy.map((a) => a.name).join(", ")}>
                              <span data-testid="picked-by" tabIndex={0}>{s.pickedBy.length} {s.pickedBy.length === 1 ? "automation" : "automations"}</span>
                            </Tooltip>
                          ) : "—"}
                        </Td>
                      ) : null}
                      <Td mono muted hideWhenNarrow><span aria-label={`ends in ${s.hint}`}>…{s.hint}</span></Td>
                      {admin ? (
                        <Td align="right">
                          <RowMenu size="sm" label={`Actions for ${s.name}`} items={[
                            { id: "value", label: "Change value…", icon: "edit", onSelect: () => open({ kind: "value", secret: s }) },
                            { id: "recipients", label: "Change who gets it…", icon: "human", onSelect: () => open({ kind: "recipients", secret: s }) },
                            { kind: "separator" },
                            { id: "remove", label: "Remove", tone: "danger", onSelect: () => open({ kind: "remove", secret: s }) },
                          ]} />
                        </Td>
                      ) : null}
                    </Tr>
                  </Tooltip>
                ))}
              </TBody>
            </Table>
            {addAction ? <FormActions>{addAction}</FormActions> : null}
          </>
        )}
        <div className="orgSecretsNotes">
          <SettingsNote icon="layers">{ORG_SECRETS_SAME_NAME(orgName)}</SettingsNote>
          <SettingsNote icon="info">{NO_TALKERS_NOTE}</SettingsNote>
          <SettingsNote icon="info">{SECRETS_NOTE}</SettingsNote>
          <SettingsNote icon="lock">{REDACTION_NOTE}</SettingsNote>
        </div>
      </SettingsSection>
      {editing?.kind === "add" ? (
        <AddSecretDialog level={level} taken={secrets.map((s) => s.name)} busy={busy} problem={saveProblem} onClose={close}
          onSubmit={(name, value, recipients) => void save(() => client.addOrganizationSecret(id, name, value, recipients), `${name} added`)} />
      ) : null}
      {editing?.kind === "recipients" ? (
        <RecipientsDialog level={level} secret={editing.secret} busy={busy} problem={saveProblem} onClose={close}
          overriddenIn={overriddenIn(secrets.find((s) => s.name === editing.secret.name) ?? { ...editing.secret, overriddenIn: [], pickedBy: null })}
          onSubmit={(recipients) => void save(() => client.changeOrganizationSecretRecipients(id, editing.secret.name, recipients),
            recipientsDone(editing.secret.name, recipients))} />
      ) : null}
      {editing?.kind === "value" ? (
        <ChangeValueDialog secret={editing.secret} busy={busy} problem={saveProblem} onClose={close}
          description="Changing the value leaves who gets it as it is. Previews and agent sessions in every project get the new value when they next start, wake or resume."
          onSubmit={(value) => void save(() => client.replaceOrganizationSecret(id, editing.secret.name, value), `${editing.secret.name} value changed`)} />
      ) : null}
      <RemoveSecretDialog editing={editing} busy={busy} problem={saveProblem} onClose={close}
        description={`New previews, agent sessions and automation runs in every ${orgName} project won't get it. Ones already running keep it until they start again.`}
        onRemove={(name) => void save(() => client.removeOrganizationSecret(id, name), `${name} removed`)} />
    </div>
  );
}
