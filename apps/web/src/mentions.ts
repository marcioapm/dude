/**
 * Who a session's composer offers to @-mention: its people first, then
 * everyone else in the organisation. In an organisation session everyone
 * can open it, so mentioning anyone reaches them; in a private one, those
 * not in it are offered `outside` (the owner may share it with them as
 * the message is sent; anyone else's mention of them stays plain text).
 */

import type { MentionItem } from "@dude/design-system/components";
import type { SessionAccess, SessionMemberView } from "@dude/domain";

export interface Mentionable {
  readonly id: string;
  readonly name: string;
  readonly photoUrl?: string | null | undefined;
}

export function mentionables(you: string, members: readonly SessionMemberView[], everyone: readonly Mentionable[],
  access: SessionAccess): MentionItem[] {
  const inIt = new Map(members.filter((m) => m.accepted).map((m) => [m.person.id, m]));
  const word = (role: string) => (role === "owner" ? "owner" : role === "read" && access === "private" ? "can read" : "can chat");
  const people: MentionItem[] = [];
  for (const m of inIt.values()) {
    if (m.person.id === you) continue;
    people.push({ person: everyone.find((p) => p.id === m.person.id) ?? m.person, detail: `In this session · ${word(m.role)}` });
  }
  for (const p of everyone) {
    if (p.id === you || inIt.has(p.id)) continue;
    people.push(access === "organisation"
      ? { person: p, detail: "Not in it yet · can open it and chat" }
      : { person: p, detail: "Not in this session", outside: true });
  }
  return people;
}
