/**
 * Email addresses as a person types or pastes them into an invite: one per
 * line, comma or semicolon (outside `<…>`), `Name <email>` keeping the
 * name, and a spreadsheet row (tab-separated) whose cell with an `@` is the
 * address and whose first other non-empty cell is the name. Addresses are
 * trimmed and lower-cased, as the server stores them.
 */

export interface EmailEntry {
  /** Stable across edits, for React and for removing one chip. */
  readonly key: string;
  /** As typed: what an entry that is not an email shows. */
  readonly raw: string;
  /** Set only when `raw` holds an address. */
  readonly email?: string | undefined;
  readonly name?: string | undefined;
}

// Deliberately loose: the server's validation decides; this only keeps
// obvious typing mistakes from becoming invitations.
const EMAIL = /^[^\s<>@,;"]+@[^\s<>@,;".]+(\.[^\s<>@,;".]+)+$/;

let counter = 0;

/** One typed entry: `Name <email>`, `"Name" <email>` or a bare address; `name` from a spreadsheet's other cell. */
export function emailEntry(text: string, name?: string): EmailEntry {
  const raw = text.trim();
  const m = /^"?([^"<]*?)"?\s*<([^>]+)>$/.exec(raw);
  const email = (m ? m[2]! : raw).trim().toLowerCase();
  const named = (m ? m[1]!.trim() : "") || name?.trim() || undefined;
  counter += 1;
  const key = `e${counter}`;
  return EMAIL.test(email) ? { key, raw, email, ...(named ? { name: named } : {}) } : { key, raw };
}

/** Pasted or typed text as entries, in order. Blank parts are dropped. */
export function parseEmailEntries(text: string): EmailEntry[] {
  const out: EmailEntry[] = [];
  for (const line of text.split(/\r?\n/)) {
    if (!line.trim()) continue;
    if (line.includes("\t")) {
      const cells = line.split("\t").map((c) => c.trim());
      const at = cells.find((c) => c.includes("@"));
      if (at) out.push(emailEntry(at, cells.find((c) => c && c !== at)));
      continue;
    }
    for (const part of line.split(/[,;](?![^<]*>)/)) if (part.trim()) out.push(emailEntry(part));
  }
  return out;
}

/**
 * What happens to one entry. `valid` and `renew` are invited; the rest are
 * left out. `member`, `invited` and `renew` are the caller's to know (who
 * is in the organisation, what is already pending); `invalid` and
 * `duplicate` are decided here.
 */
export type EmailChipKind = "valid" | "renew" | "invalid" | "member" | "invited" | "duplicate";

export interface EmailChipView {
  readonly kind: EmailChipKind;
  /** The words after the address: "already in Acme", "invitation expired 2 days ago: Invite renews it". */
  readonly note?: string | undefined;
  /** One inline action: "Copy their link". */
  readonly action?: { readonly label: string; readonly onClick: () => void } | undefined;
}

export const TAKEN: ReadonlySet<EmailChipKind> = new Set(["valid", "renew"]);

/** Each entry's view, in order: the first of an address wins and the rest are duplicates; `classify` says what the caller knows of an address. */
export function emailChipViews(entries: ReadonlyArray<EmailEntry>,
  classify: (email: string) => EmailChipView | null = () => null): EmailChipView[] {
  const seen = new Set<string>();
  return entries.map((e) => {
    if (!e.email) return { kind: "invalid", note: "not an email" };
    if (seen.has(e.email)) return { kind: "duplicate", note: "twice in the list" };
    seen.add(e.email);
    return classify(e.email) ?? { kind: "valid" };
  });
}

/**
 * The count line under the field: "3 to invite (1 renewed) · 4 left out", or
 * "" with nothing typed. Why each is left out is the reasons list's to say.
 */
export function emailChipSummary(views: ReadonlyArray<EmailChipView>): string {
  if (!views.length) return "";
  const taken = views.filter((v) => TAKEN.has(v.kind)).length;
  const renewed = views.filter((v) => v.kind === "renew").length;
  const left = views.length - taken;
  return `${taken} to invite${renewed ? ` (${renewed} renewed)` : ""}${left ? ` · ${left} left out` : ""}`;
}
