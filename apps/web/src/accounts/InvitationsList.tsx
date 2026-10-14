/**
 * An organisation's invitations by status (docs/design/accounts.md, *The
 * Invitations page*): tabs with counts, search, and each row's action —
 * pending: Copy link, Revoke…; expired: Renew (the same link); revoked:
 * Invite again. Status and search are the server's (`?status=&q=`), so a
 * pending invitation never hides past a page.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { EntityLine, Segmented } from "@dude/design-system/components";
import { Badge, Button, Callout, Dialog, Input, Table, TableEmpty, TBody, Td, Th, THead, Tr, useToast } from "@dude/design-system/primitives";
import { Icon } from "@dude/design-system";
import type { InvitationRow, InvitationStatus } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { inviteLink, useCopy, type InvitationsApi } from "./InviteDialog.tsx";
import { agoWords, roleWord, untilWords } from "./words.ts";

export type Filter = InvitationStatus | "all";
const FILTERS: readonly Filter[] = ["pending", "expired", "accepted", "revoked", "all"];
const STATUS: Record<InvitationStatus, { label: string; tone: "info" | "attention" | "success" | "neutral" }> = {
  pending: { label: "Pending", tone: "info" },
  expired: { label: "Expired", tone: "attention" },
  accepted: { label: "Accepted", tone: "success" },
  revoked: { label: "Revoked", tone: "neutral" },
};
const LABEL: Record<Filter, string> = { pending: "Pending", expired: "Expired", accepted: "Accepted", revoked: "Revoked", all: "All" };
/** The rows the API returns per page; a `cursor` says there are more. */
export const INVITATIONS_PAGE = 50;

/** How many one page says there are: past a page, one more than a page, which `countWord` reads as "50+". */
export const countOf = (r: { invitations: readonly unknown[]; cursor: string | null }) =>
  r.cursor ? INVITATIONS_PAGE + 1 : r.invitations.length;

/** A count as the tabs and the menu say it: "50+" past one page. */
export const countWord = (n: number) => (n > INVITATIONS_PAGE ? `${INVITATIONS_PAGE}+` : String(n));

export interface InvitationsListProps {
  api: InvitationsApi;
  orgName: string;
  /** Bumped by the page when it invited someone: read again. */
  version: number;
  /** Invite again: the invite dialog with this address. */
  onInviteAgain: (row: InvitationRow) => void;
  /** Names a person by id (who accepted, who revoked), when the page knows them. */
  personName?: ((id: string) => string | undefined) | undefined;
  /** The counts changed (the menu's "3 pending"). */
  onCounts?: ((counts: Record<Filter, number>) => void) | undefined;
}

export function InvitationsList({ api, orgName, version, onInviteAgain, personName, onCounts }: InvitationsListProps) {
  const [filter, setFilter] = useState<Filter>("pending");
  const [query, setQuery] = useState("");
  const [rows, setRows] = useState<InvitationRow[] | null>(null);
  const [counts, setCounts] = useState<Record<Filter, number> | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<InvitationRow | null>(null);
  const [changed, setChanged] = useState(0);
  const copy = useCopy();
  const { toast } = useToast();
  const reads = useRef(0);

  // The shown rows: the server filters and searches; only the latest read lands.
  useEffect(() => {
    const read = ++reads.current;
    const timer = setTimeout(() => {
      api.list({ status: filter, q: query.trim() }).then(
        (r) => read === reads.current && (setRows(r.invitations), setProblem(null)),
        (err: unknown) => read === reads.current && setProblem(errorText(err)),
      );
    }, query ? 200 : 0);
    return () => clearTimeout(timer);
  }, [api, filter, query, version, changed]);

  // Each tab's count, for the whole organisation, not the search.
  const onCountsRef = useRef(onCounts);
  onCountsRef.current = onCounts;
  useEffect(() => {
    let current = true;
    void Promise.all(FILTERS.map((s) => api.list({ status: s }).then((r) => [s, countOf(r)] as const))).then(
      (pairs) => {
        if (!current) return;
        const next = Object.fromEntries(pairs) as Record<Filter, number>;
        setCounts(next);
        onCountsRef.current?.(next);
      }, () => undefined);
    return () => {
      current = false;
    };
  }, [api, version, changed]);

  const act = useCallback((what: Promise<unknown>, done: string, description?: string) => {
    what.then(() => {
      toast({ title: done, ...(description ? { description } : {}), tone: "success" });
      setChanged((n) => n + 1);
    }, (err: unknown) => toast({ title: errorText(err), tone: "danger" }));
  }, [toast]);

  // A count past one page reads "50+": the API pages by 50 and says only that there are more.
  const tabCount = (f: Filter) => (counts ? ` ${countWord(counts[f])}` : "");
  return (
    <>
      <div className="invitationFilters">
        <Segmented size="sm" label="Status" value={filter} onChange={setFilter} data-testid="status-filter"
          options={FILTERS.map((f) => ({ value: f, label: `${LABEL[f]}${tabCount(f)}` }))} />
        <Input size="sm" aria-label="Search invitations" placeholder="Search email or name" value={query} onChange={(e) => setQuery(e.target.value)}
          leading={<Icon name="search" size={14} />} className="invitationSearch" data-testid="invite-search" />
      </div>
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      <Table density="comfortable" data-testid="invitations">
        <THead><Tr><Th width="36%">Invited</Th><Th width="26%">Status</Th><Th hideWhenNarrow>Role</Th><Th hideWhenNarrow>Expires</Th><Th align="right"><span className="ds-sr-only">Actions</span></Th></Tr></THead>
        <TBody>
          {rows === null ? null : rows.length ? rows.map((r) => {
            const accepter = r.acceptedPersonId ? personName?.(r.acceptedPersonId) : undefined;
            return (
              <Tr key={r.id} data-testid="invitation-row" data-status={r.status} data-email={r.email}>
                <Td><EntityLine lead={<span className="envelope" aria-hidden><Icon name="link" size={14} /></span>} name={r.name ?? r.email}
                  detail={[r.name ? r.email : null, `by ${r.inviter === "the operator" ? "the operator" : r.inviter.split(" ")[0]}, ${agoWords(r.createdAt)}`].filter(Boolean).join(" · ")} /></Td>
                <Td wrap>
                  <span className="statusCell">
                    <Badge tone={STATUS[r.status].tone} dot>{STATUS[r.status].label}</Badge>
                    {r.status === "accepted" && r.acceptedAt ? <span className="muted">{accepter ? `by ${accepter}, ` : ""}{agoWords(r.acceptedAt)}</span>
                      : r.status === "revoked" && r.revokedAt ? <span className="muted">{agoWords(r.revokedAt)}</span> : null}
                  </span>
                </Td>
                <Td fit hideWhenNarrow>{r.role === "admin" ? <Badge>{roleWord(r.role)}</Badge> : <span className="muted">{roleWord(r.role)}</span>}</Td>
                <Td fit muted hideWhenNarrow>{r.status === "pending" ? untilWords(r.expiresAt) : r.status === "expired" ? `expired ${agoWords(r.expiresAt)}` : "—"}</Td>
                <Td align="right" fit>
                  <span className="rowActions">
                    {r.status === "pending" ? <>
                      <Button size="sm" variant="secondary" leadingIcon="copy" onClick={() => copy(inviteLink(r.code), `${r.name ?? r.email}'s link`)} data-testid="copy-link">Copy link</Button>
                      <Button size="sm" variant="quiet" onClick={() => setRevoking(r)} data-testid="revoke">Revoke…</Button>
                    </> : r.status === "expired" ? (
                      <Button size="sm" variant="secondary" leadingIcon="retry" data-testid="renew"
                        onClick={() => act(api.renew(r.id), "Renewed for 14 days", `${r.name ?? r.email}'s link works again: the same one you sent.`)}>Renew</Button>
                    ) : r.status === "revoked" ? (
                      <Button size="sm" variant="quiet" leadingIcon="plus" onClick={() => onInviteAgain(r)} data-testid="invite-again">Invite again</Button>
                    ) : null}
                  </span>
                </Td>
              </Tr>
            );
          }) : <TableEmpty colSpan={5}>{query.trim() ? `No invitation matches “${query.trim()}”.` : `No ${filter === "all" ? "" : `${filter} `}invitations to ${orgName}.`}</TableEmpty>}
        </TBody>
      </Table>
      {revoking ? (
        <Dialog open onOpenChange={(o) => !o && setRevoking(null)} tone="danger" size="sm" title={`Revoke ${revoking.name ?? revoking.email}'s invitation?`}
          description={`The link stops working at once; opening it says it can't be used. If they already joined, this changes nothing. You can invite ${revoking.email} again later, with a new link.`}
          footer={<>
            <Button variant="quiet" onClick={() => setRevoking(null)}>Cancel</Button>
            <Button variant="danger" solid data-testid="revoke-confirm" onClick={() => {
              act(api.revoke(revoking.id), "Invitation revoked");
              setRevoking(null);
            }}>Revoke</Button>
          </>} />
      ) : null}
    </>
  );
}
