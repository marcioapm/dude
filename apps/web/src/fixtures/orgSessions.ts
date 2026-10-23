/**
 * `dude.fixtures.run` = org-sessions: sessions open to the organisation.
 * `#/sessions/ssn_csv` is one you own, open to everyone, with Ana's
 * message mentioning Tom; `#/sessions/ssn_macros` is one whose owner left,
 * owned by the admins (you are one); `#/sessions/open` lists them with a
 * third you never joined; Waiting on you has Ana's mention of you. In the
 * shapes the API returns (orchestrator/internal/api/sessions.go).
 */

import type { OpenSessionsList, SessionDetail, SessionsList } from "@dude/domain";
import { PEOPLE, PROJECT, YOU } from "./data.ts";

const MIN = 60_000;
const iso = (ago: number) => new Date(Date.now() - ago).toISOString();
const ref = (id: string) => {
  const p = PEOPLE.find((x) => x.id === id)!;
  return { id: p.id, name: p.name, photoUrl: p.photoUrl, online: p.online };
};
const member = (id: string, role: "owner" | "chat" | "read", open = false) =>
  ({ person: ref(id), role, accepted: true, becomesOwner: false, open });
const projects = [{ id: PROJECT.id, key: PROJECT.key, name: PROJECT.name, repositories: [{ id: "repo_wc", name: "web-console", defaultBranch: "main" }] }];

export const ORG_SESSION = "ssn_csv";
export const ADMIN_OWNED_SESSION = "ssn_macros";

function detail(id: string, title: string, people: SessionDetail["session"]["people"], extra: Partial<SessionDetail["session"]>,
  you: SessionDetail["you"]): SessionDetail {
  return {
    session: { id, title, titledBy: "agent", createdAt: iso(90 * MIN), people, projects, run: null, runs: [], costUsd: 0.08, messages: 2,
      access: "organisation", ownedByAdmins: false, ...extra },
    you, proposals: [], question: null, agentMachines: [],
  };
}

export const ORG_SESSIONS: Record<string, SessionDetail> = {
  [ORG_SESSION]: detail(ORG_SESSION, "Usage CSV export times out for October",
    [member(YOU, "owner", true), member("u_ana", "chat"), member("u_tom", "read")], {},
    { id: YOU, role: "owner", archived: false, member: true, admin: true }),
  [ADMIN_OWNED_SESSION]: detail(ADMIN_OWNED_SESSION, "Support macros cleanup",
    [member("u_sam", "owner"), member("u_kai", "chat"), member("u_ana", "chat")], { ownedByAdmins: true },
    { id: YOU, role: "owner", archived: false, member: false, admin: true }),
};

export function orgSessionsList(): SessionsList {
  const row = (d: SessionDetail, role: "owner" | "chat") => ({
    id: d.session.id, title: d.session.title, role, createdAt: d.session.createdAt, owner: d.session.people[0]!.person, shared: true,
    projects, runStatus: "completed", dudePause: null, filed: 0, lastActivityAt: iso(19 * MIN), archived: false,
    access: d.session.access ?? "private", ownedByAdmins: d.session.ownedByAdmins ?? false,
  });
  return {
    sessions: [row(ORG_SESSIONS[ORG_SESSION]!, "owner"), row(ORG_SESSIONS[ADMIN_OWNED_SESSION]!, "owner")],
    invitations: [], questions: [],
    mentions: [{ id: "1", sessionId: "ssn_retry", title: "Thoughts on the export retry policy", by: ref("u_ana"),
      text: "@Márcio Martins can you check how many rows per meter October has?", createdAt: iso(2 * MIN), access: "organisation", member: false }],
    organisation: { count: 4, joined: 2 },
  };
}

export function openSessionsList(): OpenSessionsList {
  const open = (id: string, title: string, owner: string, joined: boolean, ownedByAdmins = false, runStatus = "completed", archivedByOwner = false) =>
    ({ id, title, owner: ref(owner), joined, people: 3, projects, runStatus, lastActivityAt: iso(19 * MIN), ownedByAdmins, archivedByOwner });
  return {
    sessions: [
      open(ORG_SESSION, "Usage CSV export times out for October", YOU, true, false, "running"),
      open(ADMIN_OWNED_SESSION, "Support macros cleanup", "u_sam", true, true),
      open("ssn_retry", "Thoughts on the export retry policy", "u_ana", false),
      open("ssn_onboarding", "Onboarding checklist for support", "u_tom", false, false, "completed", true),
    ],
    joined: 2,
  };
}
