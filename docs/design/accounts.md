# Accounts across organisations

Status: design, 2026-10-13. Mockup: `mockups/accounts/index.html`.

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
- **Access puts everyone in one organisation.** With
  `auth.provider = cloudflare_access`, `memberFor` matches the verified email
  to the people of `auth.default_organization`, and with `auth.auto_create`
  makes anyone Access admits a member there. There is no second
  organisation for an Access sign-in to land in.
- **No way to be in two organisations.** A key resolves to exactly one
  organisation (`lookup_api_key`), and so does an Access sign-in. Nothing in
  the web app could show a second one.

Migration 001 sketched the right shape — `users` (global, email unique) and
`org_memberships` — and nothing ever wrote to either; `people` replaced them
in practice.

## Decided

Márcio, 2026-10-13:

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

## The records

The smallest change from today: one new global table for the human, one
for invitations, and `people` keeps being what every other table points at.

### `accounts` — who you are, everywhere

| column | |
|---|---|
| `id` | `acc_…` |
| `email` | `citext`, unique. The identity: Access's verified email, lower-cased |
| `name` | as everyone sees you |
| `photo_url`, `photo_key`, `photo_token` | as `people` has them today (053): an Access picture URL, or an uploaded image in storage |
| `created_at`, `signed_in_at` | the second touched at most once an hour, by a browser sign-in |

Global, like `organizations`: no `organization_id`, no row-level security.
The backend reads it only through `withoutTenant` in two places — sign-in
and the super-admin routes — and through `SECURITY DEFINER` functions that
take an account id (`account_memberships`, below). The orchestrator never
reads it.

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

**Coming back.** Re-invited after removal, a person gets their old row back
(`removed_at` cleared, role from the invitation), so their history and their
new work are one person. Their old keys stay revoked.

### `invitations` — someone asked to join

| column | |
|---|---|
| `id` | `inv_…` |
| `organization_id` | tenant-scoped, row-level security as `people` |
| `email` | `citext` |
| `name` | optional, from a pasted `Name <email>`: used only if Access gives no name |
| `role` | `admin` \| `member` |
| `invited_by` | a person of the organisation; null when the super-admin invited |
| `created_at`, `sent_at`, `expires_at` | `sent_at` moves on Resend; `expires_at` is `sent_at` + 14 days |
| `accepted_at`, `accepted_person_id` | the membership it became |
| `revoked_at`, `revoked_by` | |

`UNIQUE (organization_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL`:
one open invitation per email per organisation. An expired one still holds
that place, so inviting the same email again is Resend on the same row, not
a second row. Inviting someone who is already an active member is a 409
naming them.

No code and no link secret: Access has already proved the email, so an
invitation is matched by email when its person signs in. What the admin
passes on is the deployment's address, which is not a secret.

`invitations_for_email(email)` (`SECURITY DEFINER`) returns the open,
unexpired invitations of every organisation for one email, with the
organisation's name and slug, for sign-in.

### `api_keys` — unchanged

A key keeps `organization_id` and `person_id`. Since a person is one
organisation's, a key already means "this account, in this organisation";
nothing is added. What changes is who makes keys: only the person
themselves, in Your settings. An invitation makes none.

Removing someone revokes their keys in that organisation only, as now; their
keys elsewhere keep working.

`api_keys_person` (035), which makes a person for a key made without one,
stays for the operator's scripts until `scripts/seed-demo.ts` and
`add-person.ts` make accounts; it is the source of email-less people, so it
goes in the same change that retires them.

### Super-admins — a list in the configuration

`auth.super_admins` (`DUDE_AUTH_SUPER_ADMINS`): emails, comma-separated in
the environment. Optional, default empty.

A column (`accounts.super_admin`) was the alternative. The list wins:

- **It has no bootstrap problem.** A fresh deployment has no accounts; the
  operator's email in the configuration is a super-admin the moment they
  first sign in.
- **The application cannot grant it.** No API route or SQL that `dude_app`
  runs can make someone a super-admin; only whoever deploys can.
- **It is where the operator already is.** The configuration file lives in
  the deployment's repository, reviewed and versioned; a column would need a
  page to manage it, guarded by the very power it grants.
- Against: changing it is a restart. Super-admins change rarely.

A super-admin is not a member of anything by being one: they read no
organisation's tasks, sessions or settings. They create organisations, see
who is in each, invite, and make someone admin when an organisation has
none. To work in an organisation they invite themselves, like anyone else.

Super-admin routes (`/v1/admin/…`) take only an Access sign-in, never a
key: a key implies one organisation, and this is outside all of them.

### Migration of what exists

One migration, in both processes' terms (people ids unchanged, so the
orchestrator needs nothing):

1. One account per distinct lower-cased `people.email`, removed rows
   included. Name and photo from that email's active row seen most recently
   (`last_seen_at`), else its newest row.
2. `people.account_id` set by email.
3. **People without an email get no account.** They keep working through
   their keys, exactly as today, and cannot sign in with a browser until
   they have an email. The super-admin page lists them per organisation
   ("Signs in only with a key") with **Set email…**, which sets
   `people.email`, finds or makes that account and links it. On Márcio's
   deployment that is the seeded first admin: set `marcio@…`, sign in, and
   it is the same person with all its history.
4. `account_id` stays nullable until no active person lacks one; a later
   migration makes it `NOT NULL` for active rows.

`auth.default_organization` and `auth.auto_create` are retired (accepted for
one release with a warning, per *Configuration stays compatible* in
CONTRIBUTING): an Access sign-in now lands in the organisations its account
is in. Nobody already in an organisation notices; someone who would have
been auto-created now sees *No organisation* and asks for an invitation.

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
  route (`#/sessions`, `#/waiting`), and the same shape works if the app
  moves from hash to path routing.
- **Old links without an organisation** (`#/task/wi_…`, in Slack, in
  notifications) still open: ids are unique across organisations, so the app
  asks `GET /v1/locate/:id`, which looks in each of your memberships, and
  rewrites the URL. Not yours anywhere: Not found, as now.
- **The backend resolves it per request**: Access token → account by email
  → the active person of that account in the named organisation → principal
  (`personId`, `role`), then `withOrg` with `set_config('app.organization_id')`
  per transaction, exactly as today. Not a member: 403 `not_a_member`. No
  header: the organisation if the account has one, else 400
  `organization_required`. Account routes (`/v1/me`, `/v1/me/keys`,
  `/v1/me/invitations`) need none.
- **A key implies its organisation.** A header that names another is a 403,
  so a script pointed at the wrong organisation fails loudly instead of
  acting in the key's.
- **Switching is navigation.** No server state changes, no session is
  re-issued; the app goes to `#/o/<slug>/` and remounts what it read for the
  other organisation (projects, people, the stream). The last organisation
  is remembered per browser (`dude.org`) only to choose where a bare `#/`
  opens.

## Flows

What the person sees at each step. "Access" is Cloudflare's sign-in page in
front of dude.

### First sign-in, no membership, no invitation

1. They open dude; Access asks them to sign in; they do.
2. dude finds no account for the email, and no invitation. It makes no
   account (an account exists only once someone invited it or it joined).
3. **No organisation** (screen 2): "Your email isn't in any organisation on
   this dude", the email they signed in with, who runs this dude (the first
   super-admin's name and email), **Check again** and **Sign out**. No key
   field.
4. With an invitation that has expired, the page says so instead: "Your
   invitation to Northwind expired on 2 October. Ask Ana Ribeiro to send it
   again."

### First sign-in, one invitation

1. Access sign-in.
2. dude finds one open invitation. Signing in is accepting it: the account is
   made (name and picture from Access, else the invitation's name, else the
   email), the membership too, the invitation marked accepted.
3. **Welcome to Acme** (screen 1), inside Acme's shell: you as others will
   see you, editable on the spot; the organisation, your role and who invited
   you; its projects, the ones with tasks of yours first; how dude works in
   three lines; one primary action, **Start your first session**, which opens
   Acme's welcome with the composer focused.
4. The page shows once per membership (`people.welcomed_at`); after, dude
   opens on the welcome as everyone's does.

### First sign-in, two invitations

1. Access sign-in.
2. Two open invitations: joining is a choice, so nothing is accepted yet.
3. **You're invited** (screen 1, second variant), outside any organisation:
   your profile, then one card per invitation (organisation, role, who
   invited you, when it expires) with **Join** and **Not now**.
4. Joining the first opens its welcome page. The other stays in the
   organisation switcher, under *Invitations*, until joined, declined or
   expired.

### Switching organisation

1. Open the switcher (screen 3): variant A in the sidebar's top, variant B in
   the profile band. Keyboard: Tab to it, Enter or Space opens, arrows move,
   Enter picks, Escape closes.
2. It lists your organisations, each with your role, the current one ticked;
   then invitations waiting; then, for a super-admin, **All organisations**.
3. Picking Northwind goes to `#/o/northwind/`: Northwind's welcome, its
   projects in the sidebar, its people online, its *Waiting on you*. A tab
   still on Acme stays on Acme.

### Inviting someone (admin)

1. Organisation settings → Members → **Invite people** (screen 5).
2. One field takes emails: one, several separated by commas or lines, or
   pasted `Name <email>` lines from a mail client. Each is shown as it will be
   invited, an unreadable line marked and left out, an existing member named
   ("already in Acme") and left out.
3. Role, then **Invite 3 people**. No secret is shown.
4. They appear under *Pending*, each with its expiry. The dialog's last line
   and each pending row's **Copy link** give the address to send them:
   "They sign in at dude.acme.dev with this email."

### Accepting

The same as first sign-in: an invitation is accepted by signing in with its
email (one) or by **Join** (several, or later from the switcher). A person
already in Acme who is invited to Northwind sees it in the switcher under
*Invitations* and on Your settings → Organisations.

### Revoking, expiring, resending

1. **Revoke** on a pending row: confirm, and it leaves the list; signing in
   with that email no longer joins.
2. **Expired** rows stay under *Pending* with "Expired 3 days ago" and
   **Resend**, which moves `sent_at` and the expiry 14 days on. They drop off
   the list 30 days after expiring.
3. **Resend** on an unexpired row does the same: it is how an admin extends
   one.

### Removing someone

1. Members → **Remove…** on a row.
2. "Remove Tom Okafor from Acme? His keys for Acme stop working at once.
   Tasks he owns become nobody's; what he did keeps his name. He stays
   signed in to dude for his other organisations."
3. The membership gets `removed_at`; the account stays. If Acme was his only
   organisation, his next page is *No organisation*.

### A CLI key, and using it

1. Your settings → Keys (screen 4): keys grouped by organisation, each
   group with **New key for Acme**.
2. Name it ("Laptop"), Create: the key is shown once, with Copy and the line
   to use it:
   `dude login https://dude.acme.dev` (then paste).
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

### Super-admin creates an organisation

1. Márcio opens the switcher → **All organisations**, or `#/admin`. A page
   outside every organisation (screen 6): its own left menu titled "This
   dude · Super-admin", a line saying it shows no organisation's work, and
   **Back to Acme**.
2. **Create organisation**: name, slug (filled from the name, editable, fixed
   once made: it is in every link), first admin's email.
3. The organisation is made with the default tiers and sizes a new one gets
   today, and an admin invitation for that email (`invited_by` null: the
   pending row says "Invited by the operator").
4. Opening an organisation shows its members, pending invitations, members
   who sign in only with a key (**Set email…**), and **Invite** (email and
   role) for when an organisation has lost its admins.

## What changes for the CLI and the API

- **Keys stay bearer tokens**, `Authorization: Bearer dude_sk_…`, and a key
  still implies its organisation and person. Nothing that uses a key today
  changes.
- **`X-Dude-Organization`** (and `org=` on `/v1/events/stream`) for an Access
  request; checked against the key's organisation when both are given.
- **`GET /v1/me`** gains `account` (id, email, name, photo),
  `organizations` (id, name, slug, your role, your person id) and
  `invitations`; `organization` stays, the request's.
- **New**: `GET/PATCH /v1/me` write the account; `GET /v1/me/keys` across
  memberships (one `withOrg` per membership, so row-level security holds);
  `POST /v1/me/keys {organizationId, name}`;
  `POST /v1/me/invitations/:id/accept|decline`; `GET /v1/locate/:id`.
- **Invitations**: `GET/POST /v1/organization/invitations`
  (`{invitations: [{email, name?}], role}`, many at once),
  `POST /v1/organization/invitations/:id/resend`, `DELETE …/:id`.
  `POST /v1/people` (invite-with-a-key) is retired; `insertPerson` loses its
  key.
- **Super-admin**: `GET/POST /v1/admin/organizations`,
  `GET /v1/admin/organizations/:id`, `POST /v1/admin/organizations/:id/invitations`,
  `PATCH /v1/admin/people/:id {email}` (Set email…), `PATCH …/:id {role}`.
- **The orchestrator** is untouched: it receives the organisation and person
  per call, as it does now.
- **`auth.provider = api_key`** stays for local development, the demo and the
  test suites, which have no Cloudflare in front: there the browser key
  prompt remains, labelled as a developer sign-in. Production runs Access and
  never shows it.

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
- The sign-in screen then offers the configured methods; with Access alone
  there is no screen, since Access is the screen. The mockup's
  *No organisation* page carries no method list for that reason.
- Linking a second method to an existing account is from Your settings,
  signed in; a new method's email matching an existing account never links on
  its own.

## What the design system gains

Nothing here adds a token, tone or radius. Composed in the mockup from
existing components, and wanted as components when this is built:

- **`OrgSwitcher`** — needs a DS component. The trigger (an organisation's
  face, its name, a chevron; or, in the band, `SidebarProfile` with a
  chevron action) and its menu. `RowMenu` does the menu (Radix: arrows,
  Enter, Escape, `menuitemradio` with the tick) but its radio items take no
  `leading` face; the mockup shows the role as each item's description. The
  component wants a face in radio items and a needs-you count at the end.
- **An organisation's face** — needs a DS component, or a decision:
  organisations have no image or colour slot today; the mockup uses
  `ProjectAvatar`'s initials on an identity colour.
- **`InviteList`** — needs a DS component: the parsed preview under the
  invite field (each line as it will be invited, unreadable and existing ones
  marked). Composed from `EntityLine` rows in the mockup.
- **`KeyShownOnce`** moves from the app into the DS with a `usage` slot for
  the CLI line.
- **A page outside any organisation** reuses `SettingsLayout` with its own
  scope ("This dude · Super-admin"); no new shell.

## Open questions

1. **Auto-join by domain.** With `auto_create` retired, nobody joins without
   an invitation. Keep an organisation setting "anyone signing in with an
   @acme.dev email joins as a member"? *Recommend: not now; add it when an
   organisation asks.*
2. **Who Access lets in.** dude cannot invite past Access: the Access
   application's policy must admit the invited email. Make the policy broad
   (any email from the identity providers) and let dude decide, or keep it
   narrow and add people in both places? *Recommend: broad policy, dude
   decides; the No organisation page is built for exactly that.*
3. **Invitation email.** dude sends no email today. *Recommend: no email for
   now; Copy link and the admin tells them. Revisit with notifications
   (design/notifications.md).*
4. **Each organisation's waiting count in the switcher.** A number beside
   each organisation in the menu is a hint, not an inbox. *Recommend: show
   it; the mockup's "Counts in menu" switch shows both.*
5. **Switcher placement**: A (sidebar top, where the brand is) or B (profile
   band, as llm-proxy). *Recommend A: the organisation is where you are, and
   the top is where people look for that; the band stays you. B keeps dude's
   brand at the top and is one menu for "me" (settings, sign out,
   organisations).*
6. **Invitation lifetime.** *Recommend 14 days, extended by Resend; expired
   rows kept 30 days for the "expired" message.*

## Screens

`mockups/accounts/screens/`, each in Dark and Light, desktop 1440×900
unless marked mobile (390×844):

| file | |
|---|---|
| `<Theme>-first-one.png` | first sign-in, one invitation: Welcome to Acme |
| `<Theme>-first-two.png` | first sign-in, two invitations: Join each |
| `<Theme>-none.png` | no organisation |
| `<Theme>-none-expired.png` | no organisation, an expired invitation |
| `<Theme>-switcher-A.png` | switcher A, sidebar expanded, menu open |
| `<Theme>-switcher-A-rail.png` | switcher A, collapsed, menu open |
| `<Theme>-switcher-B.png` | switcher B, sidebar expanded, menu open |
| `<Theme>-switcher-B-rail.png` | switcher B, collapsed, menu open |
| `<Theme>-switcher-after.png` | after switching to Northwind |
| `<Theme>-settings.png` | Your settings: profile, keys by organisation |
| `<Theme>-settings-reveal.png` | a new key, shown once, with the CLI line |
| `<Theme>-members.png` | Members: pending invitations, members |
| `<Theme>-members-invite.png` | Invite people, bulk paste parsed |
| `<Theme>-members-remove.png` | Remove someone |
| `<Theme>-admin.png` | super-admin: organisations |
| `<Theme>-admin-create.png` | super-admin: create organisation |
| `<Theme>-admin-org.png` | super-admin: one organisation |
| `<Theme>-mobile-first-one.png` | mobile: Welcome to Acme |
| `<Theme>-mobile-first-two.png` | mobile: two invitations |
| `<Theme>-mobile-switcher-A.png` | mobile: drawer, switcher A open |
| `<Theme>-mobile-switcher-B.png` | mobile: drawer, switcher B open |
