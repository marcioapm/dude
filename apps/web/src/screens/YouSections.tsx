/**
 * Your profile and your keys, in your settings: how teammates see you
 * (name, photo), and the keys that act as you (the CLI, scripts).
 *
 * A photo is resized here, in the browser, to a small square JPEG and sent
 * as a data: URL — dude keeps it in the database until there is file
 * storage, so it must be small.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { Duration, HumanAvatar } from "@dude/design-system/components";
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
import type { ApiClient, ApiKeyInfo, Member } from "../api/client.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";
import { photoOf } from "../hooks/usePeople.ts";

/** The photo's side, in pixels: twice the largest face drawn, for sharp screens. */
const PHOTO_PX = 160;

/** A picked image as a small square JPEG data: URL, cropped to its centre. */
export async function squarePhoto(file: File, px = PHOTO_PX): Promise<string> {
  const bitmap = await createImageBitmap(file);
  const side = Math.min(bitmap.width, bitmap.height);
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = px;
  canvas.getContext("2d")!.drawImage(bitmap, (bitmap.width - side) / 2, (bitmap.height - side) / 2, side, side, 0, 0, px, px);
  bitmap.close();
  return canvas.toDataURL("image/jpeg", 0.85);
}

export function ProfileSection({ client, me, onChanged }: { client: ApiClient; me: Member; onChanged: () => void }) {
  const [name, setName] = useState(me.name);
  const file = useRef<HTMLInputElement>(null);
  const { busy, problem, save } = useSave();

  const setPhoto = (photoUrl: string | null) =>
    void save(() => client.updateMe({ photoUrl }), onChanged, photoUrl ? "Photo updated" : "Photo removed");

  return (
    <Card data-testid="profile">
      <CardHeader title="Profile" />
      <CardBody>
        <FormStack>
          <p className="muted">How your teammates see you, on tasks and in chats.</p>
          <div className="profilePhoto">
            <HumanAvatar person={{ id: me.id, name: me.name, imageUrl: photoOf(me) }} size="lg" className="profileFace" data-testid="profile-photo" />
            <Button variant="secondary" disabled={busy} onClick={() => file.current?.click()} data-testid="photo-upload">
              Upload…
            </Button>
            {me.photoUrl ? (
              <Button variant="quiet" disabled={busy} onClick={() => setPhoto(null)} data-testid="photo-remove">
                Use initials
              </Button>
            ) : null}
            <input ref={file} type="file" accept="image/png,image/jpeg,image/webp,image/gif" hidden data-testid="photo-file"
              onChange={(e) => {
                const picked = e.target.files?.[0];
                e.target.value = "";
                if (picked) void save(async () => client.updateMe({ photoUrl: await squarePhoto(picked) }), onChanged, "Photo updated");
              }} />
          </div>
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} data-testid="profile-name" />
          <KeyValueList items={[
            { label: "Email", value: me.email ?? "—" },
            { label: "Role", value: me.role === "admin" ? "Organization admin" : "Member" },
          ]} />
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
        </FormStack>
      </CardBody>
      <CardFooter>
        <Button variant="primary" disabled={busy || !name.trim() || name.trim() === me.name} data-testid="profile-save"
          onClick={() => void save(() => client.updateMe({ name: name.trim() }), onChanged, "Profile saved")}>
          Save
        </Button>
      </CardFooter>
    </Card>
  );
}

export function KeysSection({ client }: { client: ApiClient }) {
  const [keys, setKeys] = useState<ApiKeyInfo[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [making, setMaking] = useState(false);
  const [made, setMade] = useState<{ name: string; key: string } | null>(null);
  const [revoking, setRevoking] = useState<ApiKeyInfo | null>(null);

  const load = useCallback(() => {
    void client.listMyKeys().then((k) => setKeys(k.keys), (err: unknown) => setProblem(errorText(err)));
  }, [client]);
  useEffect(load, [load]);

  return (
    <Card data-testid="keys">
      <CardHeader title="Keys" />
      <CardBody>
        <p className="muted">For the dude CLI and scripts. Each acts as you.</p>
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
        {keys ? (
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
              {keys.map((k) => (
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
        ) : null}
      </CardBody>
      <CardFooter>
        <Button variant="secondary" leadingIcon="plus" onClick={() => setMaking(true)} data-testid="key-new">New key</Button>
      </CardFooter>
      {making ? (
        <NewKeyDialog client={client} onClose={() => setMaking(false)} onMade={(k) => {
          setMaking(false);
          setMade(k);
          load();
        }} />
      ) : null}
      {made ? <KeyShownOnce title={`Key “${made.name}”`} secret={made.key} onClose={() => setMade(null)} /> : null}
      {revoking ? (
        <RevokeKeyDialog client={client} apiKey={revoking} onClose={() => setRevoking(null)} onRevoked={load} />
      ) : null}
    </Card>
  );
}

function NewKeyDialog({ client, onClose, onMade }: {
  client: ApiClient;
  onClose: () => void;
  onMade: (made: { name: string; key: string }) => void;
}) {
  const [name, setName] = useState("");
  const { busy, problem, save } = useSave();
  return (
    <FormDialog open onOpenChange={(o) => !o && onClose()} title="New key"
      description="Name it for where it will live, so you know which to revoke."
      submitLabel="Create key" submitTestId="key-create" canSubmit={!busy && Boolean(name.trim())} problem={problem}
      onSubmit={() => {
        let key = "";
        void save(async () => { key = (await client.createMyKey(name.trim())).key; }, () => onMade({ name: name.trim(), key }));
      }}>
      <Input label="Name" autoFocus value={name} placeholder="Laptop CLI" onChange={(e) => setName(e.target.value)} data-testid="key-name" />
    </FormDialog>
  );
}

/** A secret, shown this once, with a way to copy it. */
export function KeyShownOnce({ title, secret, onClose, description }: {
  title: string;
  secret: string;
  onClose: () => void;
  description?: string;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="sm" title={title}
      description={description ?? "Copy it now: it is not shown again."}
      footer={<Button variant="primary" onClick={onClose} data-testid="key-done">Done</Button>}>
      <FormStack>
        <Input label="Key" mono readOnly value={secret} onFocus={(e) => e.currentTarget.select()} data-testid="key-secret" />
        <Button variant="secondary" leadingIcon="copy" onClick={() => void navigator.clipboard?.writeText(secret).then(() => setCopied(true))}>
          {copied ? "Copied" : "Copy"}
        </Button>
      </FormStack>
    </Dialog>
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
