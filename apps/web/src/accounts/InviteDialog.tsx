/**
 * Inviting people: the chip editor, then a link per person (docs/design/
 * accounts.md, *Inviting*). Shared by an organisation's admins (Members,
 * Invitations) and the super-admin (one organisation's page): `api` says
 * which routes it goes through.
 */

import { useEffect, useMemo, useState } from "react";
import {
  EmailChipsInput, InvitationLinkList, emailChipViews, invitationLinesText, parseEmailEntries,
  type EmailChipView, type EmailEntry, type InvitationLink,
} from "@dude/design-system/components";
import { Button, Dialog, FormStack, Select, useToast } from "@dude/design-system/primitives";
import type { PersonRole } from "@dude/domain";
import type { InvitationRow, InvitationStatus, InviteResult } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { absolute, agoWords, roleWord } from "./words.ts";

/** The routes an invitations screen goes through: an organisation's own, or the super-admin's for it. */
export interface InvitationsApi {
  list: (params: { status?: InvitationStatus | "all"; q?: string; cursor?: string }) => Promise<{ invitations: InvitationRow[]; cursor: string | null }>;
  invite: (invitations: Array<{ email: string; name?: string }>, role: PersonRole) => Promise<{ results: InviteResult[] }>;
  renew: (id: string) => Promise<unknown>;
  revoke: (id: string) => Promise<unknown>;
}

const ROLES = [{ value: "member", label: "Member" }, { value: "admin", label: "Admin" }];

/** Copy to the clipboard, saying so; a browser that refuses (no permission, not secure) is told too. */
export function useCopy() {
  const { toast } = useToast();
  return (text: string, what: string) => {
    const write = navigator.clipboard?.writeText(text);
    if (!write) return toast({ title: `Could not copy ${what.toLowerCase()}: this browser does not allow it here`, tone: "danger" });
    void write.then(
      () => toast({ title: `${what} copied`, tone: "success" }),
      () => toast({ title: `Could not copy ${what.toLowerCase()}`, tone: "danger" }),
    );
  };
}

export const inviteLink = (code: string) => absolute(`#/invite/${code}`);

/** The links an invite gave back, one per invitee: created, renewed, or one already pending. */
export function madeLinks(entries: readonly EmailEntry[], results: readonly InviteResult[], role: PersonRole): InvitationLink[] {
  const names = new Map(entries.filter((e) => e.email).map((e) => [e.email!, e.name]));
  return results.filter((r) => r.invitation && (r.result === "created" || r.result === "renewed" || r.result === "existing")).map((r) => ({
    email: r.email, name: names.get(r.email), role: roleWord(r.invitation?.role ?? role), url: inviteLink(r.invitation!.code),
    renewed: r.result === "renewed",
  }));
}

export interface InviteDialogProps {
  api: InvitationsApi;
  orgName: string;
  /** The members' emails, lower-cased: their chips say "already in <org>". */
  memberEmails: readonly string[];
  /** Pre-filled: Invite again, or the first admin. */
  initial?: string | undefined;
  initialRole?: PersonRole | undefined;
  onClose: () => void;
  /** Something was invited: the lists behind the dialog read again. */
  onInvited: () => void;
}

export function InviteDialog({ api, orgName, memberEmails, initial, initialRole, onClose, onInvited }: InviteDialogProps) {
  const [entries, setEntries] = useState<EmailEntry[]>(() => (initial ? parseEmailEntries(initial) : []));
  const [role, setRole] = useState<PersonRole>(initialRole ?? "member");
  const [live, setLive] = useState<readonly InvitationRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [links, setLinks] = useState<InvitationLink[] | null>(null);
  const copy = useCopy();

  // What is already pending or expired here, so each chip can say so before Invite.
  useEffect(() => {
    let current = true;
    void Promise.all([api.list({ status: "pending" }), api.list({ status: "expired" })]).then(
      ([p, e]) => current && setLive([...p.invitations, ...e.invitations]), () => undefined);
    return () => {
      current = false;
    };
  }, [api]);

  const members = useMemo(() => new Set(memberEmails.map((e) => e.toLowerCase())), [memberEmails]);
  const views: EmailChipView[] = emailChipViews(entries, (email) => {
    if (members.has(email)) return { kind: "member", note: `already in ${orgName}` };
    const row = live.find((r) => r.email.toLowerCase() === email);
    if (row?.status === "pending") {
      return { kind: "invited", note: `already invited ${agoWords(row.createdAt)}`,
        action: { label: "Copy their link", onClick: () => copy(inviteLink(row.code), `${row.name ?? row.email}'s link`) } };
    }
    if (row?.status === "expired") return { kind: "renew", note: `invitation expired ${agoWords(row.expiresAt)}: Invite renews it` };
    return null;
  });
  const taken = entries.filter((_, i) => views[i]!.kind === "valid" || views[i]!.kind === "renew");

  const send = () => {
    setBusy(true);
    setProblem(null);
    api.invite(taken.map((e) => ({ email: e.email!, ...(e.name ? { name: e.name } : {}) })), role).then(
      ({ results }) => {
        setLinks(madeLinks(taken, results, role));
        setBusy(false);
        onInvited();
      },
      (err: unknown) => {
        setProblem(errorText(err));
        setBusy(false);
      },
    );
  };

  if (links) {
    return (
      <Dialog open onOpenChange={(o) => !o && onClose()} size="lg" title={links.length === 1 ? `1 invitation to ${orgName}` : `${links.length} invitations to ${orgName}`}
        description="dude sends no email. Send each person their link: it works only for their address, for 14 days. Cloudflare Access must let them in too."
        footer={<>
          <Button variant="secondary" leadingIcon="copy" disabled={!links.length} onClick={() => copy(invitationLinesText(links), "All links")} data-testid="copy-all">Copy all</Button>
          <Button variant="primary" onClick={onClose} data-testid="invite-done">Done</Button>
        </>}>
        <FormStack>
          <InvitationLinkList links={links} onCopy={(l) => copy(l.url, `${l.name ?? l.email}'s link`)} data-testid="invite-links" />
          <p className="muted">They're on the Invitations page too, with their links, until they join or the link expires.</p>
        </FormStack>
      </Dialog>
    );
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="lg" title={`Invite people to ${orgName}`}
      description="You get a link for each person to send them yourself. A link works only for its address."
      footerProblem={problem}
      footer={<>
        <Button variant="quiet" onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={busy || !taken.length} onClick={send} data-testid="invite-submit">
          {busy ? "Inviting…" : taken.length === 1 ? "Invite 1 person" : `Invite ${taken.length} people`}
        </Button>
      </>}>
      <FormStack>
        <EmailChipsInput entries={entries} onChange={setEntries} views={views} autoFocus summaryWords={{ member: `already in ${orgName}` }}
          placeholder="ana@acme.dev, Tom Okafor <tom@acme.dev>" data-testid="chips"
          hint="Type or paste. Enter, comma or space adds one; pasting “Name <email>” or a column from a spreadsheet keeps the names." />
        <Select label="Role" value={role} options={ROLES} onValueChange={(v) => setRole(v as PersonRole)}
          hint="For everyone in this invitation. Change one person's role on Members once they've joined." data-testid="invite-role" />
      </FormStack>
    </Dialog>
  );
}
