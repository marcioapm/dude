# Accounts across organisations

Status: design v4, 2026-10-13, revised to Márcio's answers of 13:14 the same
day: membership decides who gets in; nothing disclosed to people dude doesn't let in.
Mockup: `mockups/accounts/index.html`.

Source in `apps/web/mockups/accounts`, built with
`MOCKUP=accounts bunx vite build --config mockups/vite.config.ts` from
`apps/web`. Screens in `mockups/accounts/screens/`, listed at the end.

## The problem

A person in dude is a row of one organisation. Everything about who they
are and what they may do sits in that row, and nothing connects two of them.

- **One row is identity and membership at once.** `people` (migration 035)
  holds `organization_id`, `name`, `email`, `role`, the photo, presence and
  `removed_at`. The same human in two organisations would be two unrelated
  rows with two names, two photos and nothing saying they are one person.
  `people_email_idx` is unique per organisation, so nothing stops it, and
  nothing supports it either.
- **An invitation is a password.** `insertPerson` makes the row and a first
  API key, and Members shows the admin that key "once": the admin hands a
  secret over by chat or email, the invitee pastes it into the sign-in card,
  and from then on it lives in that browser's `localStorage`. There is no
  pending state, no expiry, no way to take an invitation back other than
  removing the person it already made.
- **The browser signs in with a pasted key.** `KeyPrompt` in
  `apps/web/src/main.tsx` asks for `dude_sk_…` and nothing else. It was the
  stand-in until real sign-in, and it still is the only door in `api_key`
  mode.
- **The first admin has no email.** A key made without a person
  (`api_keys_person`, 035; `scripts/seed-demo.ts`) makes a person named as
  the key, with `email` null. That person can never be matched to a
  Cloudflare Access sign-in: Members shows them as "Email —".
- **Access decides who is in.** With `auth.provider = cloudflare_access`,
  `memberFor` (`apps/control-plane/src/api/cloudflareAccess.ts`) matches the
  verified email to the people of `auth.default_organization`, and with
  `auth.auto_create` makes anyone Access admits a member there. Being let
  through Access is being a member; there is no second organisation for an
  Access sign-in to land in.
- **No way to be in two organisations.** A key resolves to exactly one
  organisation (`lookup_api_key`), and so does an Access sign-in. Nothing in
  the web app could show a second one.

Migration 001 sketched the right shape — `users` (global, email unique) and
`org_memberships` — and nothing ever wrote to either; `people` replaced them
in practice.

## Decided

Márcio, 2026-10-13, first round:

1. **You sign in with Cloudflare Access.** Its token says who you are (your
   email, and a picture from `get-identity`). A pasted key no longer signs a
   browser in. Email and password, and SSO (GitHub, Google, SAML), come
   later and slot in beside Access.
2. **You can be in several organisations.** Your name and photo are the
   same in all of them, for now.
3. **A key belongs to an organisation, and to you.** You make one for the
   CLI in each organisation you work in.
4. **Nobody makes their own organisation.** The deployment's operator does,
   on a super-admin page, and invites the people in.
5. **Waiting on you stays per organisation.** There is no inbox across them.

Second round, answers to v1's open questions:

6. **The switcher is in the profile band** at the bottom of the sidebar
   (v1's variant B). The brand stays at the top. The band's menu holds your
   organisations, **Join…** for each invitation waiting for you, **Your
   settings**, **All organisations** (super-admins only) and **Sign out**.
7. **No email-domain auto-join**, not yet.
8. *(v2: anyone Access lets in can join automatically. Withdrawn by 14.)*
9. **Invitations are links, not emails.** dude sends no email. An admin
   invites, gets one link per invitation (`#/invite/<code>`), and sends it
   themselves. A link is **bound to an email**: it works only for the
   address Access has proved.
10. **Inviting is a multi-chip email editor**, and the result is each
    invitee with their link, a Copy button each, and Copy all.
11. **Invitations have their own page**, beside Members, listing every
    invitation by status. The super-admin sees the same list for any
    organisation.
12. **Invitations last 14 days;** renewing gives 14 days from now.

Third round, 12:45, answers to v2's open questions:

13. **dude never adds anyone to Cloudflare Access.** Access is configured on
    its own, outside dude, and dude leaves it alone. The invite dialog's
    links say in one line that Access must also let the person in.
14. **Membership decides who gets in, not Access.** Access proves who
    someone is (their email) and nothing more. dude lets a person in only if
    they are an active member of at least one organisation, hold a pending,
    unexpired invitation, or have their email in `auth.super_admins`. **There
    is no automatic joining:** v2's per-organisation "Anyone who signs in
    joins" (`organizations.open_to_everyone`) is gone, with its page section,
    its confirm dialog and its note on Members. See *The sign-in gate*.
15. **Each organisation's waiting count stays in the switcher**, beside its
    name and your role.

Fourth round, Márcio, 2026-10-13 13:14:

> "We don't want to disclose who the super-admins are, and which organisations
> are using dude. Can we change that screen to say something generic instead?"

**Nothing disclosed to people dude doesn't let in.** They learn nothing about
this deployment's super-admins or admins, which organisations exist, or who
invited whom. This applies to every reachable screen and public or
pre-membership endpoint, including errors, not just *No organisation*.
Their own verified identity is safe to show. A matching, pending invitation
lets them in narrowly: Access proved its address, so only that invitation's
organisation and inviter may be shown. A code alone is not that proof.
The deployment's operator tells people who to ask **out of band**; dude
publishes no directory at the front door.

Recommended here, for Márcio to overrule:

16. **No open links** (invitations without an email). See *Open links*.
17. **Renew keeps the same link.** See *Renewing*.
18. **An account row is made when its person accepts their first
    invitation**, never for a stranger. See *When an account is made*.
19. **No organisation deactivation yet.** See the edge-case table.
20. **One role per invitation dialog,** no per-chip role. See *Inviting*.

## The sign-in gate

Every browser request goes through the same four lines. Access verifies its
JWT and gives the email; dude looks up the account by that email; then:

| the email has | dude lets them | lands on |
|---|---|---|
| an active membership | in, to their organisations | the organisation in the URL, else the last one |
| no membership, a pending unexpired invitation | in **only to accept**: `GET /v1/me` and the invitation routes | *You're invited* (screen 1, second variant), or the link's accept page |
| no membership, no invitation, and is in `auth.super_admins` | in **to the super-admin page** only | `#/admin` |
| none of these | nowhere; their own identity and generic guidance only | *No organisation* (screen 2), which records nothing; a link may show only the generic mismatch or unusable state |

- **Which rows answer it.** "Active membership": a `people` row with that
  account and `removed_at IS NULL`. "Pending invitation":
  `invitations_for_email(email)` returns one or more that are neither
  accepted nor revoked and whose `expires_at > now()`. The super-admin list
  is read from the configuration. The first true line wins, so a super-admin
  who is also a member sees their organisations and has **All
  organisations** in the menu; one with only an invitation sees *You're
  invited* and reaches `#/admin` too.
- **It runs per request, not per session.** dude has no session of its own
  behind Access: removing someone from their last organisation, revoking
  their last invitation or letting it expire changes the next request's
  answer, with nothing to invalidate.
- **The invited line is narrow.** Until they press Join, the routes open to
  them are `GET /v1/me` (which says `gate: "invited"` and lists the
  invitations), `GET /v1/invitations/:code` and
  `POST /v1/invitations/:code/accept`. Everything that needs an organisation
  answers 403 `not_a_member`, as it does for a member naming an organisation
  they are not in.
- **The fourth line writes nothing.** No account, no `signed_in_at`, no row
  of any table keyed by the email.
  `GET /v1/me` answers 200 `gate: "none"` with their own Access identity
  only. No expired invitation history, organisation names or super-admin list.
  `GET /v1/invitations/:code` is readable by any verified email, writes
  nothing, and returns only a mismatch state for a pending code for another
  address: no organisation, inviter or invited address. Unusable codes return
  the same generic 404. `POST …/accept` rechecks the code and email and gives
  the same generic 404 or 403 mismatch without details. Every other route
  answers 403 `not_admitted`, whether or not the requested resource exists.
- **A key is not gated by this.** A key already is a person of one
  organisation, and removing that person revokes it (`removePerson`).

### When an account is made

**Recommended: when the person accepts their first invitation**, in the
same transaction that makes or brings back the membership. The alternative
was the first sign-in while holding an invitation.

- **Never for a stranger.** An uninvited email leaves no row, so dude keeps
  no table of everyone Access ever let in. Access may admit the whole
  company, contractors and test accounts; none of that is dude's to keep, and
  a list of addresses that once reached the front door is a list someone
  will one day be asked to purge.
- **Accept-time over sign-in-time.** Someone who opens *You're invited* and
  presses **Not now**, or never comes back, holds an invitation, not an
  account. Making the row at sign-in would leave accounts that belong to no
  organisation and never did, which every account reader (the super-admin's
  lists, `account_memberships`) would have to tell apart from people who
  left. With accept-time creation an account always has, or had, at least
  one membership.
- **The cost:** the *You're invited* and accept pages show a profile that is
  not stored yet. The name and photo come from Access (`get-identity`,
  fetched between transactions as `memberFor` does today); the name field is
  editable there and travels with Join (`POST …/accept {name}`); the photo
  is changed on the welcome page, after joining.
- A super-admin with no membership has no account either. Their identity is
  the email in the configuration; they get an account the day they accept an
  invitation, their own included.

### Removed from every organisation

Their next request finds no active membership and no invitation, and lands
on *No organisation*. Their account and every `people` row stay, removed:
tasks, answers and events keep their name and face. An invitation from any
organisation brings them back (`removed_at` cleared in that organisation, or
a new row in another), as the same account.

### Holding only an expired invitation

An expired invitation lets nobody in. They see the same generic *No
organisation* page as someone never invited, even for their own verified
email. No organisation, inviter or past invitation is disclosed. Opening
its code shows the generic *can't be used* page. Renewing it (same link,
14 more days) lets them in at their next request.

## The records

The smallest change from today: one new global table for the human, one
for invitations, and `people` keeps being what every other table points at.
Organisations gain nothing.

### `accounts` — who you are, everywhere

| column | |
|---|---|
| `id` | `acc_…` |
| `email` | `citext`, unique. The identity: Access's verified email, trimmed and lower-cased |
| `name` | as everyone sees you |
| `photo_url`, `photo_key`, `photo_token` | as `people` has them today (053): an Access picture URL, or an uploaded image in storage |
| `created_at`, `signed_in_at` | the second touched at most once an hour, by a browser request that passed the gate's first line |

Global, like `organizations`: no `organization_id`, no row-level security.
The backend reads it only through `withoutTenant` in two places — the gate
and the super-admin routes — and through `SECURITY DEFINER` functions that
take an account id (`account_memberships`, below). The orchestrator never
reads it.

Made only by accepting an invitation (*When an account is made*), and by
the migration for the people who exist today.

**Not `users`.** That table (001) has the right columns but the wrong
name: dude already says "user" for a key's kind (`api_keys.kind = 'user'`,
`PrincipalKind`), and "person" for a member. Two legacy columns point at it
(`interventions.created_by`, `control_requested_by`) and one orchestrator
query reads it (`api.go`, `answered_by`). Dropping `users` and
`org_memberships` is a separate cleanup; this design neither uses nor
removes them.

### `people` — you, in one organisation

`people` stays the membership row. It keeps its id, so every task, event,
answer, push subscription and session member that names a person is
untouched; it keeps `role`, presence (`last_seen_at`: being online in Acme
says nothing about Northwind) and `removed_at`.

It gains `account_id text REFERENCES accounts(id)`, with
`UNIQUE (organization_id, account_id) WHERE removed_at IS NULL`: one active
membership per account per organisation; and `welcomed_at`, set when its
first-time page has been shown.

How someone joined (Members' *Joined* column: "invited by Ana") is read from
the invitation accepted into that row (`invitations.accepted_person_id`). A
row with none is the first admin, or joined before invitations existed (by
`auth.auto_create`): "joined before invitations". No column for it.

**The profile lives on the account; `people` keeps a copy.** `name`,
`email` and the photo columns stay on `people`, written by a trigger on
`accounts` into each **active** membership of that account. The other way
round — every reader joining `accounts` — would touch `person_ref()` and
about forty readers in both processes, and put a global table into every
tenant query. The copy costs one trigger and buys three things: nothing that
reads people changes; a removed member keeps the name and face they had
when they left, and their old organisation does not learn their new photo;
and a per-organisation profile, if it is ever wanted, is the trigger
stopping. `PATCH /v1/me` and the photo upload write the account.

**Coming back is by invitation.** Accepting an invitation to an
organisation where the account has a removed row brings that row back
(`removed_at` cleared, role from the invitation), so their history and their
new work are one person. Their old keys stay revoked.

### `invitations` — someone asked to join, by link

| column | |
|---|---|
| `id` | `inv_…` |
| `organization_id` | tenant-scoped, row-level security as `people` |
| `code` | `text`, unique. 128 random bits, base62 (22 characters): the link's `#/invite/<code>` |
| `email` | `citext NOT NULL`, trimmed and lower-cased. Not null: there are no open links |
| `name` | optional, from a pasted `Name <email>` or a spreadsheet's other cell: used only if Access gives no name |
| `role` | `admin` \| `member` |
| `invited_by` | a person of the organisation; null when the super-admin invited |
| `created_at`, `expires_at` | `expires_at` is `created_at` + 14 days |
| `renewed_at`, `renewed_by` | Renew sets `expires_at` to now + 14 days |
| `accepted_at`, `accepted_person_id` | the membership it became |
| `revoked_at`, `revoked_by` | |

`UNIQUE (organization_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL`:
one live invitation per email per organisation. An expired one still holds
that place, so inviting the same email again renews it rather than making a
second row.

**Status is derived, not stored.** Revoked if `revoked_at`, else accepted if
`accepted_at`, else expired if `expires_at <= now()`, else pending. A stored
`status` would need a job to turn *pending* into *expired* at the right
moment, or would lie between runs — and the gate reads "pending" on every
request of an invitee. llm-proxy stores `status` and still has to check
`expires_at` beside it everywhere, and its "one pending per email" index
then counts expired rows as pending (see the table below). The timestamps
are needed anyway for "accepted by whom, when". The Invitations page's
status filter is that expression in the `WHERE`, run on the server.

**The code is stored as is.** An admin must be able to copy a pending link
again from the Invitations page, so it cannot be a hash. A code alone grants
nothing: it works only for its email, which Access proves on every request.

`invitations_for_email(email)` (`SECURITY DEFINER`) returns the invitations
of every organisation for one verified email, restricted to pending,
unexpired invitations, with organisation name and slug, inviter, role,
code and expiry: the gate's second line, the menu's **Join…** and the
first-sign-in page. It never supplies expired history to *No organisation*.
`invitation_for_code(code, email)` (`SECURITY DEFINER`) returns what the
accept page may show to that email (below).

### `api_keys` — unchanged

A key keeps `organization_id` and `person_id`. Since a person is one
organisation's, a key already means "this account, in this organisation";
nothing is added. What changes is who makes keys: only the person
themselves, in Your settings. An invitation makes none.

Removing someone revokes their keys in that organisation only, as
`removePerson` does now; their keys elsewhere keep working.

`api_keys_person` (035), which makes a person for a key made without one,
stays for the operator's scripts until `scripts/seed-demo.ts` and
`add-person.ts` make accounts; it is the source of email-less people, so it
goes in the same change that retires them.

### Super-admins — a list in the configuration

`auth.super_admins` (`DUDE_AUTH_SUPER_ADMINS`): emails, comma-separated in
the environment. Optional, default empty.

A column (`accounts.super_admin`) was the alternative. The list wins:

- **It has no bootstrap problem.** A fresh deployment has no accounts and no
  organisations; the operator's email in the configuration is a super-admin
  the moment they first sign in, and the gate's third line lets them in.
- **The application cannot grant it.** No API route or SQL that `dude_app`
  runs can make someone a super-admin; only whoever deploys can.
- **It is where the operator already is.** The configuration file lives in
  the deployment's repository, reviewed and versioned; a column would need a
  page to manage it, guarded by the very power it grants.
- Against: changing it is a restart. Super-admins change rarely.

A super-admin is not a member of anything by being one: they read no
organisation's tasks, sessions or settings. They create organisations, see
who is in each and every invitation, invite, renew and revoke, remove
people, and make someone admin when an organisation has none. To work in an
organisation they invite themselves, like anyone else.

Super-admin routes (`/v1/admin/…`) take only an Access sign-in, never a
key: a key implies one organisation, and this is outside all of them.

## Bootstrap and migration

### A fresh deployment

1. No organisations, no accounts. The operator's email is in
   `auth.super_admins`; Access is set up to let them through.
2. They sign in: the gate's third line, `#/admin`, an empty list: "No
   organisations yet. Create the first one and invite its first admin."
3. **Create organisation**: name, slug, first admin's email — possibly their
   own. They get that admin's link.
4. If it is their own, they open it: Join, their account is made, and they
   are Acme's admin. Otherwise they send it, and the first admin's Join does
   the same for them.

With `auth.provider = cloudflare_access`, no organisation and an empty
`auth.super_admins`, nobody can ever pass the gate. Startup refuses it, as
it refuses a missing `auth.default_organization` today (a `ConfigError`
after the database check in `index.ts`): "auth.super_admins is empty and
there are no organisations: nobody could sign in".

### An existing deployment

One migration, in both processes' terms (people ids unchanged, so the
orchestrator needs nothing). Every current member stays a member.

1. One account per distinct lower-cased `people.email`, removed rows
   included. Name and photo from that email's active row seen most recently
   (`last_seen_at`), else its newest row.
2. `people.account_id` set by email.
3. **People without an email get no account.** They keep working through
   their keys, exactly as today, and cannot sign in with a browser until
   they have an email. The super-admin page lists them per organisation
   ("no email: signs in only with a key") with **Set email…**, which sets
   `people.email`, finds or makes that account and links it. On Márcio's
   deployment that is the seeded first admin: set `marcio@…`, sign in, and
   it is the same person with all its history.
4. `account_id` stays nullable until no active person lacks one; a later
   migration makes it `NOT NULL` for active rows.
5. **`auth.auto_create` and `auth.default_organization` retire.** Both are
   accepted for one release with a warning ("ignored: people join by
   invitation") and then removed (per *Configuration stays compatible* in
   CONTRIBUTING). The startup check that the default organisation exists
   goes with them.

**Who relied on auto-join.** Everyone `auth.auto_create` let in already has
a `people` row in the default organisation, so they are members, get an
account in step 1, and pass the gate's first line: nothing changes for
them. Removed people stay removed, as today. What changes is for people who
have never signed in: Access letting them through no longer makes them
members. They need an invitation from an admin of the organisation; until
then they see the generic *No organisation* page. The deployment's operator
tells them who to ask out of band. The release note says so, and the
operator who wants a group in at once pastes
its addresses into one invite dialog.

## How a request knows its organisation

Access says who; it does not say where. Three ways to carry the organisation:

| | shareable link | two tabs, two organisations | event stream | CLI |
|---|---|---|---|---|
| **(a) in the URL**, `#/o/<slug>/…` | opens in the right one | each tab its own | in the stream URL | n/a (the key says) |
| **(b) a cookie** for the active one | opens in whichever was last | the last switch wins in both | rides along | n/a |
| **(c) a header** from the SPA | not by itself | each tab its own | `EventSource` sends no headers | n/a |

**Recommended: (a), carried to the API by (c).** The page URL is the truth:
`#/o/acme/task/wi_…`, `#/o/northwind/`. The web app reads the slug from it
and sends the organisation's id on every API call as
`X-Dude-Organization`, and as `org=` on the event stream's URL (where the
key travels today). A cookie fails the two-tab case silently — the worst
kind — and a header alone forgets the organisation on every shared link.

- **`/o/` is a prefix, not a slug alone**, so no slug can ever collide with a
  route (`#/sessions`, `#/waiting`, `#/invite/…`), and the same shape works
  if the app moves from hash to path routing.
- **Old links without an organisation** (`#/task/wi_…`, in Slack, in
  notifications) still open: ids are unique across organisations, so the app
  asks `GET /v1/locate/:id`, which looks in each of your memberships, and
  rewrites the URL. Not yours anywhere: Not found, as now.
- **The backend resolves it per request**: Access token → the gate (above)
  → the active person of that account in the named organisation → principal
  (`personId`, `role`), then `withOrg` with `set_config('app.organization_id')`
  per transaction, exactly as today. Not a member of that one: 403
  `not_a_member`. No header: the organisation if the account has one, else
  400 `organization_required`. Account routes (`/v1/me`, `/v1/me/keys`,
  `/v1/invitations/:code`) need none.
- **A key implies its organisation.** A header that names another is a 403,
  so a script pointed at the wrong organisation fails loudly instead of
  acting in the key's.
- **Switching is navigation.** No server state changes, no session is
  re-issued; the app goes to `#/o/<slug>/` and remounts what it read for the
  other organisation (projects, people, the stream). The last organisation
  is remembered per browser (`dude.org`) only to choose where a bare `#/`
  opens.

## Edge cases, against llm-proxy

llm-proxy (`origin/main 3f44e17b`: `api/invites.rs`, `api/org_members.rs`,
`InviteAccept.tsx`, `Team.tsx`, `OrgSwitcher.tsx` and their suites, copied
read-only to `/var/tmp/accounts/llmp-ref/`) handles invitations and
membership in a product with the same shape. Each of its behaviours, and
what dude does:

| llm-proxy | dude | why |
|---|---|---|
| **A signed-in user with no organisation has no session at all.** A session is a (user, organisation) pair: `create_user_session(db, user_id, org_id)`, and every handler takes `UserSession.org_id`, not an option. Its suites give an invitee "their own org to have a session" before they can accept anything (`test_invites.py:243`, `test_org_invite_flow.py:68`). The OAuth callback that would make that organisation is not in the reference copy, so whether sign-up makes one is not visible | **Different: no session, and no organisation made for them** | dude has no session behind Access and makes no organisation for anyone (decision 4). The gate answers per request: a member is let in; an invitee only to accept; a super-admin to `#/admin`; anyone else sees *No organisation*, and nothing is written |
| Signed in holding only a pending invitation: `find_pending_invite_for_email` returns the newest one's code, one at most; login carries `mode=invite` and a redirect to that link | **Different: every invitation, and nothing else** | The gate's second line lets them reach only `/v1/me` and the invitation routes, and *You're invited* lists every pending invitation for their email, so a lost link or a second organisation is not a dead end |
| Signed in holding only an expired invitation: `find_pending_invite_for_email` skips it (`expires_at > now()`), so it is as if there were none | **Same gate, generic page** | It lets nobody in. *No organisation* discloses no expired history, organisation or inviter; the operator tells people who to ask out of band |
| Super-admin: `is_super_admin(db, config, session.user_id)` takes a session, so a super-admin is a user who is a member somewhere (the suites' `super_admin_org`) | **Different: no membership needed** | `auth.super_admins` is the third line of the gate; a fresh deployment with no organisations has a way in |
| An uninvited email that signs in | **No row** | Not a `users` row, not an account: dude keeps no table of everyone Access let in. See *When an account is made* |
| Removed from their only organisation: the `sessions` row for that organisation is deleted with the membership (`org_members.rs:673`), and the next request with the still-valid JWT is a 401 (`test_removed_member_jwt_rejected_immediately`) | **Same moment, a page instead of a 401** | The next request finds no active membership and lands on *No organisation*. The account and the removed `people` row stay, and an invitation brings them back as the same person |
| Emails are trimmed and lower-cased | **Same** | `citext` plus a trim on write; the chip editor shows the lower-cased address |
| Inviting the same email again returns the existing pending invitation and its link | **Same**, and **different for an expired one** | The chip says "already invited" and offers **Copy their link**; the API returns the existing invitation. llm-proxy reuses only an *unexpired* pending row, but its unique index (`WHERE status = 'pending'`) still counts the expired one, so the insert fails with a 500. dude renews the expired row instead: same link, 14 more days, the chip saying so |
| Inviting an existing member: 409 | **Same**, per address | One dialog invites many. The chip says "already in Acme" and the address is left out; the API reports `member` for that address and creates the rest, not a 409 for the batch |
| Public invite-info for a valid code returns the invited email, role and organisation; accepting with a different email returns 403 "sent to a different email address" | **Different on read: wrong email learns nothing; same 403 on accept** | dude's GET requires an Access-verified email. A pending code for another email returns only `invitation_for_another_email`, never the organisation, inviter, role or invited address. The page offers sign-in with the address it was sent to, or asking whoever sent it for a new link. Matching email may read that invitation's details because Access proved the invited address |
| Accepting when already a member: marked accepted, switched to the organisation, no error | **Same** | *You're already in Northwind*, **Open Northwind**. Switching is navigation in dude, so nothing is re-issued. Without auto-joining this is mostly someone opening their accepted link again |
| Expired, revoked or unknown code: the same "not found" on the public info endpoint | **Same, and on accept too** | llm-proxy's accept route then tells them apart ("no longer pending", "has expired"). dude answers both routes with the same 404, and the page says one thing: *This invitation can't be used. Ask whoever sent it for a new link.* A code is a bearer handle: telling expired from revoked from never-existed tells whoever holds one which codes are real and what happened to them, and the reader's next step is the same in all three. An *accepted* code opened by anyone but its member reads the same way |
| Revoking a revoked invitation: 404 | **Same** | Also for an accepted one. An expired invitation can be revoked (it would otherwise be renewable) |
| Members can't invite; only admins | **Same** | Members see the Members list; Invitations and Invite people are admins' (and the super-admin's) |
| An open invitation (no email) can be accepted by anyone signed in | **Different: not built** | See *Open links*. `invitations.email` is `NOT NULL` |
| Removing a member revokes their API keys; re-adding does not bring them back; service keys the member owns keep working | **Same** for personal keys; **not applicable** for service keys | `removePerson` already revokes every key of that person; re-joining by invitation brings the row back, not the keys. dude has no service accounts: `api_keys.kind = 'runner'` keys authenticate nothing (migration 014 revoked them, `lookupApiKey` refuses any left). An agent's tool token (`runs.mcp_token_hash`, `DUDE_TOOLS_TOKEN`) belongs to a Run, not a person: Runs already running on a removed person's tasks finish, and the tasks pass on (`passOnTasks`) |
| The last owner can't be removed or demoted; an admin can't remove an owner or promote to owner | **Same rule, two roles** | The last admin can't be removed or made member (`lastAdmin`, 409 "the organization needs an admin", as today). Any admin may make any member admin, make any other admin member, and remove anyone but themselves. With admin the top role, "not above yourself" has nothing to forbid. The super-admin may make anyone admin, for an organisation with none |
| The super-admin can remove members from organisations they don't belong to | **Same** | `DELETE /v1/admin/organizations/:id/people/:personId`, the same `lastAdmin` rule |
| Deactivating an organisation blocks its sessions and API keys | **Not now** | llm-proxy needs it for billing: a customer stops paying. A dude deployment is one company; an organisation that should stop is better served by removing its people, whose keys stop at once. Deactivating one in dude also has to say what happens to its running Runs, previews and schedules, which is a design of its own. No `deactivated_at`, no accept-page state for it |
| A removed member's session stops working at once, not when it expires | **Same** | dude has no session to expire: every request resolves Access → the gate → the *active* person in the named organisation, so the next request after removal is 403 `not_a_member` in that organisation (or *No organisation*, if it was the last). An open event stream is closed when it delivers the `PersonRemoved` event for its own person |
| Switching to an organisation you're not in: 403 | **Same** | `X-Dude-Organization` naming an organisation without your active person: 403 `not_a_member` |

## Open links

**Recommended: not built.** An open link (llm-proxy's invitation without an
email) lets anyone signed in who has it join. Now that membership is the
gate, an open link would be the one way past it that nobody chose a person
for: it is forwarded beyond the group it was meant for, and the admin cannot
see who it was meant for. The chip editor takes a pasted column of fifty
addresses and returns fifty links, which serves "this group" and keeps every
invitation bound to a person. If an organisation asks for one later, it is
`email NULL` plus a use limit; nothing here prevents it.

## Flows

What the person sees at each step. "Access" is Cloudflare's sign-in page in
front of dude, and every dude page, the accept page included, is behind it.

### First sign-in, no invitation

1. They open dude; Access asks them to sign in; they do.
2. The gate finds no account, no invitation for their email, and the email
   is not a super-admin's. Nothing is written.
3. **No organisation** (screen 2): "Your email isn't in any organisation on
   this dude", their own email, and "dude lets people in by invitation."
   "If someone invited you, open the link they sent you. Links expire after
   14 days; ask them for a new one." "Signed in with the wrong address? Sign
   out and sign in with the one you were invited with." **Check again** and
   **Sign out**. No key field or directory. "dude keeps no record of this
   visit." The deployment's operator tells people who to ask out of band.
4. An expired or revoked invitation makes no difference: the same generic
   page, no organisation or inviter named. The expired variant is dropped.

### First sign-in from a link

1. Chloé opens Gus's link, `#/invite/Nw5h…`. Access asks her to sign in.
2. The gate: no account, a pending invitation for her email. She is let in
   to accept.
3. The accept page (screen 7, *First sign-in*): "Join Northwind as member ·
   invited by Gus Lindqvist", for her address, expiring in 12 days, and a
   note: she isn't in any organisation on this dude yet; Join makes her
   account with the name and photo Cloudflare Access gave. **Join
   Northwind** and **Not now**.
4. Join: her account and membership are made, the invitation accepted, and
   she lands on **Welcome to Northwind** (screen 1), inside Northwind's
   shell: herself as others will see her, editable on the spot; who invited
   her; its projects; how dude works in three lines; **Start your first
   session**.
5. Not now: *You're invited*, with this invitation and any other waiting;
   until she joins one, that is all dude shows her.
6. Each welcome page shows once per membership (`people.welcomed_at`).

### First sign-in at the bare address, invitations waiting

She lost the links, or was sent two. The link is the delivery, not the
authority: the email is. So dude lists every pending invitation for her
verified email.

1. **You're invited** (screen 1, second variant), outside any organisation:
   "dude lets people in by invitation", her profile as Access gave it (name
   editable, sent with Join), one card per invitation (organisation, role,
   who sent it, when it expires) with **Join** and **Not now**.
2. Join on one goes to its welcome; the other waits in her menu as **Join
   Globex…** until it expires. It does not join for her: joining is the
   invitee's choice.
3. With one invitation, the same page with one card.

### The accept page, `#/invite/<code>`

Outside any organisation, dude's face at the top, "Signed in as … · Sign
out" at the bottom. `GET /v1/invitations/:code` decides, for the signed-in
email, in this order:

| the invitation | shows |
|---|---|
| unknown, revoked, or expired | *This invitation can't be used. Ask whoever sent it for a new link.* **Open dude** |
| accepted, and you are not its active member | the same *can't be used* page |
| pending, for another email | *This invitation is for a different email address.* **Accept invitation**, centred (for a mismatching email this starts Access logout, `/cdn-cgi/access/logout`; reopen the link after signing in with the invited address, then accept — it never accepts as the wrong person), "or ask whoever sent it for a new link." Their own signed-in email only; no organisation, inviter or invited address. Never Join |
| pending or accepted, for your email, and you are its active member | *You're already in Northwind*: marked accepted if pending; **Open Northwind** |
| pending, for you | *Join Northwind as member · invited by Gus*; for, role, expiry; **Join** and **Not now**. With no account yet, the note that Join makes it |

A matching pending email may see the organisation and inviter: they were
invited, and Access proved that address. No invitation details are sent
before the email check; the client cannot merely hide them.

Join is `POST /v1/invitations/:code/accept {name?}`, which re-runs the same
checks in one transaction (the same 404 and 403), makes the account if there
is none, makes or brings back the membership with the invitation's role,
sets `accepted_at`, and the app goes to `#/o/<slug>/welcome`. On a phone the
page is one column (screens *mobile-accept-join*, *mobile-accept-wrong*).

### Switching organisation

1. Open the menu from your face in the profile band (screen 3), or from
   your face at the foot of the collapsed rail, which carries the
   organisation's face in its corner. Keyboard: Tab to it, Enter or Space
   opens, arrows move, Enter picks, Escape closes.
2. It lists your organisations, each with your role and what waits on you
   there ("Admin · 2 waiting on you"), the current one ticked; then
   **Join…** for each invitation waiting for you (a dot on the band says one
   is there); **Your settings**; **All organisations** for a super-admin;
   **Sign out**.
3. Picking Northwind goes to `#/o/northwind/`: Northwind's welcome, its
   projects in the sidebar, its people online, its *Waiting on you*. A tab
   still on Acme stays on Acme.
4. **Join Globex…** opens Globex's accept page.

### Inviting (admin)

1. Organisation settings → Members → **Invite people** (screen 5), or the
   same button on Invitations. Members says how people join: "People join
   Acme by invitation. Cloudflare Access must also let them in; dude doesn't
   manage that."
2. **Emails**: type or paste. Enter, a comma, a space or leaving the field
   make a chip of what is typed; a paste is split by line, comma and
   semicolon, and a line with tabs is a spreadsheet row whose cell with an
   `@` is the address and whose first other cell is the name.
   `Name <email>` keeps the name. Backspace in the empty field takes the last
   chip back; each chip has its ✕.
3. Each chip says what will happen to it:
   - **valid**: ✓, name and address;
   - **not an email**: red, "not an email", left out;
   - **already a member**: struck through, "already in Acme", left out;
   - **already invited**: "already invited 4 days ago" and **Copy their
     link**, left out;
   - **invitation expired**: "invitation expired 2 days ago: Invite renews
     it", invited (renewed, same link);
   - **duplicate**: struck through, "twice in the list", left out.
   Under the field: "3 new · 1 renewed · 1 not an email · 1 already in Acme
   · 1 already invited · 1 duplicate. The other 4 are left out."
4. **Role** applies to everyone in the dialog. Per-chip roles are not
   offered: an invitation of mixed roles is rare, a role menu on each chip
   crowds every chip for it, and two dialogs (or a role change on Members
   after they join) cover it.
5. **Invite 4 people**: the dialog becomes **4 invitations to Acme**: each
   invitee's name, address and role, their link, and **Copy**; **Copy all**
   copies one line per person, `Name, email, link`, ready for a chat
   message or a spreadsheet. "dude sends no email. Send each person their
   link: it works only for their address, for 14 days. Cloudflare Access
   must let them in too."

### The Invitations page (admin)

Organisation settings → **Invitations** (screen 6), beside Members, with the
pending count in the menu.

- One row per invitation: name and address, who invited them and when; the
  **status** (pending, expired, accepted — by whom, when, linked to the
  member — or revoked, by whom, when); the role; the expiry ("in 10 days",
  "expired 2 days ago").
- **Filter** by status, pending first by default, with counts; **search**
  by email or name. Both are query parameters
  (`?status=pending&q=kai`), applied in SQL with the page's limit after
  them, so a pending invitation never hides past a page (llm-proxy learned
  this: `list_invites_for_org`'s server-side `status` filter).
- **Row actions**: pending — **Copy link**, **Revoke…**; expired —
  **Renew**; revoked — **Invite again** (the invite dialog, that address
  filled in: a new invitation, a new link); accepted — none.
- The super-admin sees the same list, filters and actions on any
  organisation's page.

### Renewing

**Renew** sets `expires_at` to now + 14 days and `renewed_at`; the link
stays the same. Recommended over a new link because:

- the link is already in the invitee's chat or inbox; a new one means the
  admin sends again and the old message dies, which is the mistake renewing
  exists to fix;
- a link works only for its email, so changing it buys no security;
- the admin who does want the old link dead has **Revoke**, then **Invite
  again**, which gives a new code.

### Revoking

**Revoke…** on a pending row: "The link stops working at once; opening it
says it can't be used. If they already joined, this changes nothing. You can
invite them again later, with a new link." The row stays, *Revoked by Ana, 5
days ago*. If it was the only thing letting them in, their next request
lands on *No organisation*.

### Removing someone

1. Members → **Remove…** on a row.
2. "Remove Tom Okafor from Acme? Their keys for Acme stop working at once,
   and so does their next page in Acme. Tasks they own pass to the next
   person on them; what they did keeps their name. To bring them back,
   invite them again; their old keys stay revoked. If Acme is their only
   organisation, dude stops letting them in."
3. The membership gets `removed_at`; the account stays. If Acme was their
   only organisation and no invitation waits for them, their next page is
   *No organisation*.

### A CLI key, and using it

1. Your settings → Keys (screen 4): keys grouped by organisation, each
   group with **New key for Acme**.
2. Name it ("Laptop"), Create: the key is shown once, with Copy and the line
   to use it: `dude login https://dude.acme.dev` (then paste).
3. `dude login` asks for the key, calls `GET /v1/me` with it, and stores it
   under the organisation's slug (`~/.config/dude/credentials.toml`,
   `[acme]`). Logging in with a Northwind key adds `[northwind]`.
4. The CLI acts in the organisation of the key it sends: the last one logged
   in, or `--org northwind` / `DUDE_ORG`. `dude whoami` prints the account,
   organisation and role.

Today's `dude` binary is the agent's CLI inside a Run's container
(`orchestrator/cmd/dude`); a person's CLI with `login` does not exist yet.
This is the shape it takes when it does; nothing about keys has to change
for it.

### Super-admin

1. Márcio opens his menu → **All organisations**, or `#/admin`; a
   super-admin in no organisation lands there directly. A page outside every
   organisation (screen 8): its own left menu titled "This dude ·
   Super-admin", a line saying it shows no organisation's work, and **Back
   to Acme** when they have one. The list shows each organisation's slug,
   members and pending invitations, admins (or *No admin yet*), and when it
   was made. Empty: "No organisations yet. Create the first one and invite
   its first admin."
2. **Create organisation**: name, slug (filled from the name, editable, fixed
   once made: it is in every link), first admin's email, which may be
   theirs. **Create and get the link**: the organisation is made with the
   default tiers and sizes a new one gets today, and an admin invitation for
   that email (`invited_by` null: "by the operator"); the dialog shows that
   link.
3. **One organisation**: its members, with **Set email…** for those who sign
   in only with a key, role, and **Remove…**; its Invitations, as the
   organisation's own page; **Invite people**. There is no "who can join"
   setting: every organisation is joined by invitation.

## What changes for the CLI and the API

- **Keys stay bearer tokens**, `Authorization: Bearer dude_sk_…`, and a key
  still implies its organisation and person. Nothing that uses a key today
  changes.
- **`X-Dude-Organization`** (and `org=` on `/v1/events/stream`) for an Access
  request; checked against the key's organisation when both are given.
- **`GET /v1/me`** answers every email Access verified, with the gate's
  answer: `gate` (`member` | `invited` | `super_admin` | `none`), and
  `superAdmin` (their own boolean, omitted for `none`). A member gets `account` (id, email, name, photo),
  `organizations` (id, name, slug, your role, your person id, waiting on
  you), `invitations` (pending for your email, with their codes) and
  `organization`, the request's. An invitee gets `invitations` and the
  profile Access gave. `none` gets only `gate: "none"` and their own
  verified identity (email, name and photo from Access), even if an old
  account exists. No `expiredInvitations`, `superAdmins`, organisation
  names or inviter details. It joins nobody to anything.
- **New**: `PATCH /v1/me` writes the account; `GET /v1/me/keys` across
  memberships (one `withOrg` per membership, so row-level security holds);
  `POST /v1/me/keys {organizationId, name}`; `GET /v1/locate/:id`.
- **Accepting**: `GET /v1/invitations/:code` (the accept page's state, for
  the signed-in email) and `POST /v1/invitations/:code/accept {name?}`.
  Access only, no organisation header. The menu's **Join…** goes to the same
  page.
- **Invitations** (admins): `GET /v1/organization/invitations?status=&q=&cursor=`;
  `POST /v1/organization/invitations {invitations: [{email, name?}], role}`
  → one result per address, `created` \| `renewed` \| `existing` \| `member`
  \| `invalid` \| `duplicate`, with the link for the first three;
  `POST …/:id/renew`; `DELETE …/:id` (revoke). `POST /v1/people`
  (invite-with-a-key) is retired; `insertPerson` loses its key.
- **Super-admin**: `GET/POST /v1/admin/organizations`;
  `GET /v1/admin/organizations/:id`;
  `GET/POST /v1/admin/organizations/:id/invitations`, `…/:invId/renew`,
  `DELETE …/:invId`; `PATCH /v1/admin/people/:id {email | role}`;
  `DELETE /v1/admin/organizations/:id/people/:personId`.
- **`memberFor`** stops creating people. It becomes the gate's first line:
  find the account's active person in the named organisation, and fill an
  empty name or photo from `get-identity` as it does now.
- **The orchestrator** is untouched: it receives the organisation and person
  per call, as it does now.
- **`auth.provider = api_key`** stays for local development, the demo and the
  test suites, which have no Cloudflare in front: there the browser key
  prompt remains, labelled as a developer sign-in. Production runs Access and
  never shows it.

### Pre-membership and public response contract

These are the only browser API routes available before membership (aside
from a configured super-admin's authorised admin routes). Access verification
is required for all three. Unverified requests get only 401, no deployment
identity or invitation information. There is no public invite-info endpoint.
Static sign-in/generic pages and public health responses contain no people,
organisation directory or invitation metadata. All rejected routes check
admission before resource lookup: their errors contain no names, addresses,
counts or existence hints about other people or organisations.

| endpoint / caller | returns |
|---|---|
| `GET /v1/me`, `gate: "none"` | 200: `gate: "none"` and only their own Access identity (email, name, photo). No account history, organisations, invitations or super-admin list |
| `GET /v1/me`, `gate: "invited"` | 200: their own Access profile, gate, own super-admin boolean, and only pending unexpired invitations for their verified email: code, organisation id/name/slug, role, inviter display name (or "the operator"), sent time and expiry. No expired/revoked history, members or projects |
| `GET /v1/me`, `gate: "super_admin"` | 200: their own Access identity, gate and own super-admin boolean; no account required. Other people and organisations only through the authorised admin routes |
| `GET /v1/invitations/:code`, pending wrong email | 403: `invitation_for_another_email` only; no organisation, inviter, invited address, role or dates. Available even with gate `none`; read-only |
| `GET /v1/invitations/:code`, pending matching email | 200: that invitation's code, organisation id/name/slug, role, inviter display name (or "the operator"), sent time, expiry and their own invited email. They were invited and Access proved the address |
| `GET /v1/invitations/:code`, unknown/expired/revoked, or accepted without its active membership | Identical 404 `invitation_unusable`, no metadata. An accepted code is readable only by its matching active member, not a pre-membership caller |
| `POST /v1/invitations/:code/accept`, matching pending email | Creates/reinstates their account and membership atomically; returns their own membership and its organisation id/name/slug for navigation. At this point they are a member |
| `POST /v1/invitations/:code/accept`, wrong email or unusable code | Same detail-free 403 mismatch or generic 404 as GET; no writes. Matching active-member repeat acceptance returns their own existing membership without changing its role |
| All other routes, `gate: "none"` / `"invited"` | 403 `not_admitted` / `not_a_member`, respectively, regardless of the named resource's existence. No directory, keys, profile writes, locator results, project counts or invitation history |

## Later: other ways to sign in

An account will have several ways in. When the second one arrives:

- `account_sign_ins (account_id, method, subject, created_at, last_used_at)`,
  method `access` \| `password` \| `github` \| `google` \| `saml`; `subject`
  is the provider's stable id (Access's `sub`, GitHub's user id). Access
  rows are written on sign-in from then on; the email stays the account's
  key for matching invitations.
- Access is a front door dude does not own: it hands dude a token per
  request. Password and SSO sign-ins need dude's own browser sessions, a
  table of their own. Not `sessions`: that word is brainstorm sessions.
  `sign_in_sessions (id, account_id, method, created_at, expires_at,
  revoked_at)` behind an `HttpOnly` cookie, checked where `accessAuthenticator`
  is today.
- **The gate does not change.** A new method proves an email, as Access
  does; membership, a pending invitation or `auth.super_admins` still
  decide. A password sign-up with no invitation reaches *No organisation*
  and, like a stranger behind Access, leaves no account.
- The sign-in screen then offers the configured methods; with Access alone
  there is no screen, since Access is the screen. The *No organisation* page
  carries no method list for that reason.
- Linking a second method to an existing account is from Your settings,
  signed in; a new method's email matching an existing account never links on
  its own.

## What the design system gains

Nothing here adds a token, tone or radius. Composed in the mockup from
existing components, and wanted as components when this is built:

- **`OrgSwitcher`** in the profile band — needs a DS component. The trigger
  is `SidebarProfile`'s anatomy (your face, your name, the organisation's
  face and name under it, a dot when an invitation waits, a sort chevron);
  collapsed, your face with the organisation's in its corner. `RowMenu` does
  the menu (Radix: arrows, Enter, Escape, `menuitemradio` with the tick) but
  its radio items take no `leading` face; the mockup shows the role and the
  waiting count as each item's description. The component wants a face in
  radio items and the waiting count at the end.
- **`EmailChipsInput`** — needs a DS component. `HostChips` is the nearest:
  chips with ✕, Enter / comma / space commit, Backspace takes one back. It
  lacks what invitations need: a name beside the address, a state per chip
  (valid, not an email, member, invited, renewing, duplicate) told by icon
  and words, not colour alone, an inline action ("Copy their link"), paste
  parsing of `Name <email>` and spreadsheet rows, and a summary line. The
  mockup composes it from a field box on `Input`'s tokens, `Icon` and
  `IconButton`.
- **`InvitationLinkList`** — needs a DS component: each invitee, their link
  in mono, Copy; on a phone the link drops under the name. Used by the
  invite result and by Create organisation.
- **`KeyShownOnce`** moves from the app into the DS with a `usage` slot for
  the CLI line.
- **An organisation's face** — needs a DS component, or a decision:
  organisations have no image or colour slot today; the mockup uses
  `ProjectAvatar`'s initials on an identity colour.
- Status badges with a dot and the status filter (`Segmented` with counts)
  are existing components. A page outside any organisation reuses
  `SettingsLayout` with its own scope ("This dude · Super-admin"); no new
  shell.

## Open questions

None remaining from v3. The wrong-email read exception is decided: any
Access-verified email may read the generic mismatch state, never the
organisation, inviter or invited address.

## Screens

`mockups/accounts/screens/`, each in Dark and Light, desktop 1440×900
unless marked mobile (390×844):

| file | |
|---|---|
| `<Theme>-first-one.png` | first sign-in, joined Northwind from Gus's link: Welcome to Northwind |
| `<Theme>-first-two.png` | first sign-in at the bare address: two invitations, and nothing else let in |
| `<Theme>-none.png` | no organisation: own email and generic invitation guidance only |
| `<Theme>-switcher.png` | the menu from the profile band, sidebar expanded, waiting counts |
| `<Theme>-switcher-rail.png` | the menu from the collapsed rail |
| `<Theme>-switcher-after.png` | after switching to Northwind |
| `<Theme>-settings.png` | Your settings: profile, keys by organisation |
| `<Theme>-settings-reveal.png` | a new key, shown once, with the CLI line |
| `<Theme>-members.png` | Members: people join by invitation |
| `<Theme>-members-invite.png` | Invite people: email chips in every state |
| `<Theme>-members-links.png` | after Invite: each invitee's link, Copy, Copy all |
| `<Theme>-members-remove.png` | Remove someone |
| `<Theme>-invitations.png` | Invitations, pending |
| `<Theme>-invitations-all.png` | Invitations, every status with its row actions |
| `<Theme>-invitations-search.png` | Invitations, searched |
| `<Theme>-invitations-revoke.png` | Revoke an invitation |
| `<Theme>-accept-join.png` | accept page: join |
| `<Theme>-accept-first.png` | accept page on a first sign-in: no account yet, Join makes it |
| `<Theme>-accept-wrong.png` | accept page: signed in with another email |
| `<Theme>-accept-member.png` | accept page: already in Northwind |
| `<Theme>-accept-invalid.png` | accept page: can't be used |
| `<Theme>-admin.png` | super-admin: organisations |
| `<Theme>-admin-create.png` | super-admin: create organisation |
| `<Theme>-admin-create-link.png` | super-admin: the first admin's link |
| `<Theme>-admin-org.png` | super-admin: one organisation, its members and invitations |
| `<Theme>-mobile-first-one.png` | mobile: Welcome to Northwind |
| `<Theme>-mobile-first-two.png` | mobile: two invitations |
| `<Theme>-mobile-switcher.png` | mobile: drawer, menu open |
| `<Theme>-mobile-accept-join.png` | mobile: accept page, join |
| `<Theme>-mobile-accept-wrong.png` | mobile: accept page, another email |
