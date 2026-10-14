/**
 * Your profile and your keys, in your settings: how teammates see you
 * (name, photo), and the keys that act as you (scripts, the HTTP API).
 *
 * A photo is resized in the browser and uploaded as an image (FacePicker).
 */

import { useCallback, useEffect, useState } from "react";
import { Duration, KeyShownOnce, OrgFace, PersonAvatar, roleWord } from "@dude/design-system/components";
import {
  Button,
  Callout,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  Dialog,
  FormStack,
  Input,
  KeyValueList,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
} from "@dude/design-system/primitives";
import type { PersonRole } from "@dude/domain";
import type { ApiClient, ApiKeyInfo, Me } from "../api/client.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";
import { FacePicker } from "./FacePicker.tsx";

export function ProfileSection({ client, me, onChanged, organizations, accessEmail }: {
  client: ApiClient;
  me: Me;
  onChanged: () => void;
  /** The organisations that see this profile: named when there are several. */
  organizations?: readonly string[] | undefined;
  /** Signed in through Cloudflare Access: the email is Access's, and not changed here. */
  accessEmail?: boolean | undefined;
}) {
  const [name, setName] = useState(me.name);
  const [githubLogin, setGithubLogin] = useState(me.githubLogin ?? "");
  const [commitEmail, setCommitEmail] = useState(me.commitEmail ?? "");
  const { busy, problem, save } = useSave();
  const changes = {
    ...(name.trim() !== me.name ? { name: name.trim() } : {}),
    ...(githubLogin.trim() !== (me.githubLogin ?? "") ? { githubLogin: githubLogin.trim() } : {}),
    ...(commitEmail.trim() !== (me.commitEmail ?? "") ? { commitEmail: commitEmail.trim() } : {}),
  };
  const seenBy = organizations && organizations.length > 1
    ? `The same in every organisation you're in: ${listWords(organizations)} see this name and photo.`
    : "How your teammates see you, on tasks and in chats.";

  return (
    <Card data-testid="profile">
      <CardHeader title="Profile" />
      <CardBody>
        <FormStack>
          <p className="muted" data-testid="profile-scope">{seenBy}</p>
          <FacePicker
            testId="photo"
            face={<PersonAvatar person={me} size={56} ring={false} data-testid="profile-photo" />}
            hasImage={Boolean(me.photoUrl)}
            busy={busy}
            onPick={(image) => void save(async () => client.uploadMyPhoto(await image), onChanged, "Photo updated")}
            onRemove={() => void save(() => client.updateMe({ photoUrl: null }), onChanged, "Photo removed")}
          />
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} hint="How everyone sees you, in every organisation." data-testid="profile-name" />
          <Input label="GitHub username (optional)" value={githubLogin} onChange={(e) => setGithubLogin(e.target.value)}
            placeholder="octocat" autoComplete="off" spellCheck={false} data-testid="profile-github-login" />
          <Input label="Commit email (optional)" type="email" value={commitEmail} onChange={(e) => setCommitEmail(e.target.value)}
            placeholder={me.email ?? "you@example.com"} data-testid="profile-commit-email" />
          <p className="muted" data-testid="profile-github-hint">
            Agents' commits on your tasks name you as a co-author by these: the commit email if set, else your GitHub noreply
            address, else your sign-in email.
          </p>
          <KeyValueList data-testid="profile-facts" items={accessEmail ? [
            { label: "Email", value: <span>{me.email ?? "—"} <span className="muted">· from Cloudflare Access, can't be changed here</span></span> },
            { label: "Signs in with", value: "Cloudflare Access" },
          ] : [
            { label: "Email", value: me.email ?? "—" },
            { label: "Role", value: me.role === "admin" ? "Organisation admin" : "Member" },
          ]} />
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
        </FormStack>
      </CardBody>
      <CardFooter>
        <Button variant="primary" disabled={busy || !name.trim() || Object.keys(changes).length === 0} data-testid="profile-save"
          onClick={() => void save(() => client.updateMe(changes), onChanged, "Profile saved")}>
          Save
        </Button>
      </CardFooter>
    </Card>
  );
}

/** "Acme and Northwind", "Acme, Globex and Northwind". */
function listWords(names: readonly string[]): string {
  return names.length < 2 ? names.join("") : `${names.slice(0, -1).join(", ")} and ${names.at(-1)}`;
}

/** One organisation's keys, under its face, with New key for it. */
export interface KeyGroup {
  id: string;
  name: string;
  slug: string;
  role: PersonRole;
}

/**
 * Your keys, grouped by the organisation each acts in, each group with New
 * key for it. A key made here is shown once, with a request that uses it.
 */
export function KeysSection({ client, groups }: {
  /** Reads your keys across organisations: the credential naming none. */
  client: ApiClient;
  groups: readonly KeyGroup[];
}) {
  const [keys, setKeys] = useState<ApiKeyInfo[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [making, setMaking] = useState<KeyGroup | null>(null);
  const [made, setMade] = useState<{ name: string; key: string; group: KeyGroup } | null>(null);
  const [revoking, setRevoking] = useState<ApiKeyInfo | null>(null);

  const load = useCallback(() => {
    void client.listMyKeys().then((k) => setKeys(k.keys), (err: unknown) => setProblem(errorText(err)));
  }, [client]);
  useEffect(load, [load]);
  // A key's own list names no organisation: it is the key's, the one group.
  const keysOf = (g: KeyGroup) => (keys ?? []).filter((k) => (k.organizationId ?? groups[0]?.id) === g.id);

  return (
    <Card data-testid="keys">
      <CardHeader title="Keys" />
      <CardBody>
        <FormStack>
          <p className="muted">For scripts and the HTTP API: send a key as a bearer token. A key acts as you in one organisation; make one in each you work in.</p>
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
          {groups.map((g) => {
            const own = keysOf(g);
            return (
              <section key={g.id} className="keyGroup" data-testid="key-group" data-org={g.slug} aria-label={`Keys for ${g.name}`}>
                <div className="keyGroupHead">
                  <OrgFace org={g} size={20} aria-hidden />
                  <b>{g.name}</b>
                  <span className="muted">{roleWord(g.role)}</span>
                  <span className="spacer" />
                  <Button size="sm" variant="secondary" leadingIcon="plus" onClick={() => setMaking(g)} data-testid="key-new" data-org={g.slug}>
                    New key for {g.name}
                  </Button>
                </div>
                {keys === null ? null : own.length ? (
                  <Table>
                    <THead>
                      <Tr>
                        <Th>Name</Th>
                        <Th>Key</Th>
                        <Th>Last used</Th>
                        <Th align="right"><span className="ds-sr-only">Actions</span></Th>
                      </Tr>
                    </THead>
                    <TBody>
                      {own.map((k) => (
                        <Tr key={k.id} data-testid="key-row" data-key-name={k.name}>
                          <Td>{k.name}{k.current ? <span className="muted"> · this browser</span> : null}</Td>
                          <Td mono muted>{k.prefix}…</Td>
                          <Td muted>{k.lastUsedAt ? <><Duration ms={Date.now() - Date.parse(k.lastUsedAt)} format="age" /> ago</> : "never"}</Td>
                          <Td align="right" fit>
                            {k.current ? null : (
                              <Button size="sm" variant="quiet" onClick={() => setRevoking(k)} data-testid="key-revoke">Revoke…</Button>
                            )}
                          </Td>
                        </Tr>
                      ))}
                    </TBody>
                  </Table>
                ) : <p className="muted" data-testid="no-keys">No keys for {g.name} yet.</p>}
              </section>
            );
          })}
        </FormStack>
      </CardBody>
      {making ? (
        <NewKeyDialog client={client} group={making} onClose={() => setMaking(null)} onMade={(k) => {
          setMade({ ...k, group: making });
          setMaking(null);
          load();
        }} />
      ) : null}
      {made ? (
        <KeyShownOnce title={`Key “${made.name}” for ${made.group.name}`} secret={made.key} onClose={() => setMade(null)}
          usage={`curl -H "authorization: Bearer ${made.key}" ${window.location.origin}/v1/me\n# acts as you in ${made.group.name}; its answer names the organisation and your role`} />
      ) : null}
      {revoking ? (
        <RevokeKeyDialog client={client} apiKey={revoking} onClose={() => setRevoking(null)} onRevoked={load} />
      ) : null}
    </Card>
  );
}

function NewKeyDialog({ client, group, onClose, onMade }: {
  client: ApiClient;
  group: KeyGroup;
  onClose: () => void;
  onMade: (made: { name: string; key: string }) => void;
}) {
  const [name, setName] = useState("");
  const { busy, problem, save } = useSave();
  return (
    <FormDialog open onOpenChange={(o) => !o && onClose()} title={`New key for ${group.name}`}
      description={`It acts as you in ${group.name} only. Name it for where it will live, so you know which to revoke.`}
      submitLabel="Create key" submitTestId="key-create" canSubmit={!busy && Boolean(name.trim())} problem={problem}
      onSubmit={() => {
        let key = "";
        void save(async () => { key = (await client.createMyKey(name.trim(), group.id)).key; }, () => onMade({ name: name.trim(), key }));
      }}>
      <Input label="Name" autoFocus value={name} placeholder="Deploy script" onChange={(e) => setName(e.target.value)} data-testid="key-name" />
    </FormDialog>
  );
}

function RevokeKeyDialog({ client, apiKey, onClose, onRevoked }: {
  client: ApiClient;
  apiKey: ApiKeyInfo;
  onClose: () => void;
  onRevoked: () => void;
}) {
  const { busy, problem, save } = useSave();
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} tone="danger" size="sm" title={`Revoke “${apiKey.name}”?`}
      description="Anything using it stops working at once."
      footer={
        <>
          <Button variant="quiet" onClick={onClose}>Cancel</Button>
          <Button variant="danger" solid disabled={busy} data-testid="key-revoke-confirm"
            onClick={() => void save(() => client.revokeMyKey(apiKey.id), () => {
              onClose();
              onRevoked();
            }, "Key revoked")}>
            Revoke key
          </Button>
        </>
      }>
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
    </Dialog>
  );
}
