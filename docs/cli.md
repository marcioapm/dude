# The dude CLI

`dude` drives a dude server from your terminal, as you: it records tasks,
starts them, answers agents' questions, steers runs, merges pull requests,
and reaches any route of the HTTP API. Output is indented JSON, for people
and scripts alike (`| jq`).

It is not the `dude` inside an agent's container (`orchestrator/cmd/dude`),
which acts as its Run and has other commands. This one is
`orchestrator/cmd/dude-cli`, installed under the name `dude`.

## Install

From a release, unpack `dude-cli_<version>_<os>_<arch>.tar.gz` (darwin or
linux, arm64 or amd64) and put `bin/dude` on your `PATH`:

```sh
tar -xzf dude-cli_v1.2.3_darwin_arm64.tar.gz -C ~/.local
```

From a checkout, with Go:

```sh
cd orchestrator && go build -o ~/.local/bin/dude ./cmd/dude-cli
```

It is one static binary. On Linux, `secret-tool` (`apt install
libsecret-tools`, `dnf install libsecret`) lets it keep keys in your keyring;
without it, see [Where keys are kept](#where-keys-are-kept).

## Log in

1. In dude, **Your settings → Keys → New key for <organisation>**. Name it
   for where it will live ("Laptop"); copy it, it is shown once.
2. Log in with the dude's address, and paste the key when asked (it is not
   shown as you type or paste it):

   ```sh
   dude login https://dude.acme.dev
   ```

   or pipe it, from a password manager or CI: `op read … | dude login https://dude.acme.dev`.

`login` sends the key to `GET /v1/me`. A key the server refuses is not kept.
The answer names the key's organisation, and the key is kept under that
organisation's slug. Commands act in it from then on:

```json
{
  "keyIn": "macOS keychain",
  "organization": { "name": "Acme", "slug": "acme" },
  "person": { "email": "ana@acme.dev", "id": "per_…", "name": "Ana", "role": "admin" },
  "url": "https://dude.acme.dev"
}
```

### More than one organisation

A key acts as you in one organisation. Make one in each you work in, and
log in with each. The last one logged in to is the current one.

```sh
dude login https://dude.acme.dev        # an Acme key
dude login https://dude.acme.dev        # a Northwind key: now current
dude orgs                               # both; "current": true on northwind
dude use acme                           # act in Acme from now on
dude --org northwind list WEB           # in Northwind, this once
DUDE_ORG=northwind dude list WEB        # the same, for a script
dude whoami                             # account, organisation, role
dude logout northwind                   # forget its key
```

`--org` goes before the command.

### Where keys are kept

| OS | The key | The organisation and its URL |
|---|---|---|
| macOS | the login keychain, a generic password named `HOST/SLUG` | `~/.config/dude/credentials.toml` |
| Linux, BSD | the Secret Service keyring (GNOME Keyring, KWallet, KeePassXC) through `secret-tool`, attribute `service=HOST/SLUG` | `~/.config/dude/credentials.toml` |
| Windows | the Credential Manager, a generic credential `dude:HOST/SLUG` | `%AppData%\dude\credentials.toml` |
| Any, without a keystore (a server, a container, SSH with no keyring) | `credentials.toml` itself | `credentials.toml` |

`$XDG_CONFIG_HOME` moves the file. It is mode 0600, in a 0700 directory:

```toml
current = 'acme'

[acme]
url = 'https://dude.acme.dev'

[northwind]
url = 'https://dude.acme.dev'
key = 'dude_sk_…'   # only when there is no keystore, or with login --file
```

`dude login URL --file` keeps the key in the file even where there is a
keystore. `logout` removes the key from wherever it is. The key is never
printed, and never passed on a command line, not even to `security` or
`secret-tool`.

### For scripts and CI

`DUDE_API_KEY` is used instead of the stored key, and needs no login.
`DUDE_URL` names the server; it defaults to the logged-in organisation's,
else `https://dude.absmartly.dev`.

With a stored profile, changing `DUDE_URL`
requires `DUDE_API_KEY` too: otherwise the CLI refuses to send the stored
key to a different server (the same server in another case, with its
default port or without `https://` is fine). Unset `DUDE_URL` to use the
profile's server.

```sh
DUDE_URL=https://dude.acme.dev DUDE_API_KEY=$KEY dude list WEB --status review
```

A key kept before `login` existed, as a macOS keychain item named after the
host (`security add-generic-password -s dude.absmartly.dev -w KEY`), is still
used while nothing is logged in. `dude login` replaces it.

## Arguments

- **PROJECT**: a key (`WEB`), slug, name or id.
- **TASK**: a key (`WEB-12`) or id (`wi_…`).
- **RUN**: a run id (`run_…`), or a TASK, meaning its newest agent run in
  the latest attempt (not a preview). Runs are ordered by attempt, creation
  time, then id, all descending.
- **EPIC**, **FINDING**, **PR**, **QUESTION**, **REQUEST**: ids, from the
  commands that list them (`epics`, `findings`, `prs`, `questions`,
  `repo-requests`).
- A text argument of `-` reads stdin. Use a heredoc for anything long.

`dude --help` lists every command. `dude COMMAND --help` gives its options.
`dude --version` gives the version.

## Commands

### Signing in and people

| Command | Does |
|---|---|
| `login URL [--file]` | Checks a key, then keeps it for its organisation |
| `logout [ORG]` | Forgets an organisation's key (default: the current one) |
| `orgs` | The organisations logged in to |
| `use ORG` | Switches the current organisation |
| `whoami` | The account, organisation and role in use |
| `people` | The organisation's people |
| `invite EMAIL... [--role member\|admin]` | Invites people (admins) |

### Reading

| Command | Does |
|---|---|
| `projects` | The projects you can see |
| `repos PROJECT` | A project's repositories |
| `epics PROJECT` | A project's epics |
| `overview PROJECT` | A project's board, by epic |
| `list PROJECT [--status S]` | A project's tasks: `received`, `queued`, `running`, `awaiting_input`, `review`, `ready_to_merge`, `done`, `failed`, `aborted` |
| `show TASK` | Status, why it waits (escalation), goal, criteria, runs |
| `raw TASK` | The task as the API returns it |
| `relations TASK` | What blocks it, what it blocks, what it relates to |
| `prs TASK` | Its pull requests |
| `findings [TASK] [--run RUN] [--status open]` | Review findings |
| `questions [TASK] [--run RUN]` | Agents' questions; with neither argument, every open one |
| `metrics TASK` | Cost and time |
| `events TASK [--run RUN] [--after N] [--limit N]` | Event history; pass `nextCursor` as `--after` for the next page |
| `artifacts TASK` | Files its agents published |
| `servers TASK` | Its dev servers and preview |
| `repo-requests [TASK]` | Agents' requests for another repository |
| `run RUN` | One run in full |
| `diff RUN` | What a run changed |
| `directives RUN` | What people told a run |
| `session ID` | An agent session and its children |

### Projects and epics

| Command | Does |
|---|---|
| `project-create --name N --slug s [--key K] [--description D] [--repo NAME=URL]...` | A new project (admins) |
| `repo-add PROJECT --name N --url URL [--branch main]` | Adds a repository |
| `epic-create PROJECT --title T [--description D]` | A new epic |
| `epic-update EPIC [--title] [--description] [--state planned\|active\|done\|none] [--position N]` | Changes an epic |
| `epic-delete EPIC` | Deletes an epic; its tasks stay in the project |
| `epic-archive EPIC [--ended-only]` / `epic-unarchive EPIC` | Archives an epic with its tasks (`--ended-only`: only its ended ones), or brings it back |
| `epic-metrics EPIC` | An epic's cost and time |
| `peers EPIC` | What each task on it is doing |
| `threads EPIC` | Conversations between its agents |
| `notes EPIC` | Notes pinned on it, which every agent on it reads |
| `note EPIC TEXT [--replaces N]` | Pins a note |
| `note-retire EPIC N` | Unpins note N |

### Tasks

| Command | Does |
|---|---|
| `create PROJECT --title T --goal G [--criterion C]... [--repo NAME[:read]]... [--epic ID] [--blocked-by T]... [--blocks T]... [--related T]... [--start \| --talk]` | Records a task. The goal needs 16+ characters. `--repo` is needed when the project has more than one repository. |
| `create … --repo NAME --from-pr N [--mode continue\|review]` / `--from-branch B [--base B]` | A task that takes over existing work in that repository: a pull request (number or URL) or a branch. `review` only reviews it. |
| `update TASK [--title] [--goal] [--criterion C]... [--no-criteria] [--epic ID\|none] [--repo R]... [--owner PERSON]` | Changes a task. `--criterion` and `--repo` replace all. |
| `start TASK [--override --note N]` | Delivery: agents implement, review, and open a PR |
| `talk TASK [--override --note N]` | Its conductor plans it with you in Chat; nothing is built yet |
| `auto-start TASK [--how deliver\|talk]` / `no-auto-start TASK` | Starts it once its blockers merge |
| `relate TASK --blocks T \| --blocked-by T \| --related T [--why W]` | Relates two tasks |
| `unrelate TASK blocks\|blocked_by\|related T` | Removes a relation |
| `assign TASK PERSON...` | Who is on it; the first is the owner |
| `decider TASK policy\|conductor [--open-pr] [--draft]` | Who takes its delivery decisions; `--draft` opens the pull request as a draft |
| `retry TASK` | Retries a task whose conductor failed |
| `recover TASK [resume\|retry\|restart] [--note N] [--override]` | A stopped task: without an action, the ways out; with one, takes it |
| `reset-machine TASK ROLE [--note N]` | Throws away its agent machine for ROLE (e.g. `implementer`) |
| `preview TASK [--stop]` | Starts or stops its preview environment |
| `done TASK` | Marks finished work with nothing to merge done |
| `stop TASK [--restart [--decider policy\|conductor]] [--note N]` | Stops all of its work where it stands; `--restart` starts its next attempt |
| `close TASK wont_do\|duplicate\|cant_reproduce [--duplicate-of T] [--close-pr] [--remove-relations] [--note N]` | Ends it without doing it |
| `reopen TASK` | Reopens a closed task |
| `archive TASK [--resolution R] [--duplicate-of T] [--note N]` / `unarchive TASK` | Archives it (one not ended needs `--resolution`, and is closed with it), or brings it back |

### Talking to the agents

| Command | Does |
|---|---|
| `chat TASK TEXT [--aside]` | Tells the task's conductor; `--aside` is for people only |
| `answer QUESTION [TEXT] [--choice N]...` | Answers an agent's question |
| `decide TASK ACTION [--note N]` | Your decision when delivery stopped for you; `show` lists the actions |
| `steer RUN TEXT [--interrupt]` | A running agent reads it at its next step; `--interrupt` discards its current turn |
| `pause RUN [--hard] [--reason R]`, `resume RUN`, `abort RUN [--reason R]` | Pauses, resumes or ends a run |
| `restart RUN NOTE [--tier T]` | A fresh run in its slot, told what to do differently |
| `leave RUN` | Leaves a stalled run as it is |

### Reviews and pull requests

| Command | Does |
|---|---|
| `resolve FINDING resolved\|accepted\|open [--note N]` | Settles a finding; `accepted` leaves it as it is |
| `merge PR [--method merge\|squash\|rebase]` | Merges (default: the organisation's method) |
| `update-branch PR` | Brings its branch up to date with its base |
| `rerun-checks PR` | Reruns its failed checks |
| `resolve-conflicts PR` | Has an agent resolve its merge conflicts |
| `reviewers PR LOGIN...` | Requests reviews |
| `repo-decide REQUEST approve\|decline [--note N]` | Settles an agent's request for a repository |
| `review-questions TASK` | Questions reviewers asked the task's owner; they hold delivery until settled |
| `review-answer QUESTION TEXT` | Answers one (the task's owner) |
| `review-withdraw QUESTION [--reason R]` | Withdraws one unanswered (the task's owner) |
| `verification TASK` | How its work was verified, criterion by criterion |

### Feedback on a task's work

Feedback is your own review of what the agents built. Items start as
drafts, which only you see. Submitting sends them to the task's agents in
one batch. Open items block the pull request and dude's merge until they
are resolved.

| Command | Does |
|---|---|
| `feedback TASK [--attempt N]` | Everyone's submitted items, your drafts, and how many block |
| `feedback-draft TASK TEXT` | A draft about the work in general |
| `feedback-draft TASK TEXT --path FILE --lines N[-M] [--side new\|old] [--run RUN]` | A draft on lines of a run's diff (default: the task's newest run, the new side) |
| `feedback-submit TASK [DRAFT,…]` | Sends drafts: the ones named, or all of yours |
| `feedback-send TASK TEXT` | Drafts one general item and sends it at once |
| `feedback-reply ITEM TEXT` | Replies on an item |
| `feedback-resolve ITEM [--note N]` / `feedback-reopen ITEM [TEXT]` | Resolves an item, or reopens it, saying why |
| `feedback-delete DRAFT` | Deletes one of your drafts |

### Brainstorms

A brainstorm is an agent to think something through with; it writes no code.

| Command | Does |
|---|---|
| `brainstorms [--archived]` | Your brainstorms |
| `brainstorm MESSAGE [--title T] [--project P]... [--tier T]` | Starts one |
| `brainstorm-show ID` | One in full |
| `brainstorm-chat ID TEXT` | Says something |
| `brainstorm-answer ID QUESTION [TEXT] [--choice N]...` | Answers its question |
| `brainstorm-file ID PROPOSAL ITEM...` | Files a proposal's items (by index) as tasks |
| `brainstorm-invite ID PERSON... [--role chat\|read]` | Invites people |
| `brainstorm-title ID TITLE`, `brainstorm-retry ID`, `brainstorm-archive ID`, `brainstorm-unarchive ID` | Renames, retries, archives, restores |

### Memory

| Command | Does |
|---|---|
| `memory-search QUERY [--project P] [--type T,…] [--limit N] [--words]` | Searches what agents and people remembered (`--words`: by word, not meaning) |
| `memories [--project P] [--text T] [--archived]` | Lists memories |
| `memory ID` | One in full |
| `memory-add --title T --content C [--kind fact\|procedure\|note] [--project P]` | Remembers something |
| `memory-archive ID`, `memory-restore ID` | Archives or restores |

### dude itself

| Command | Does |
|---|---|
| `guide [PAGE]` | dude's guide: its pages, or one in full (the same text agents search). `dude --help` is this CLI's help. |
| `judgements [--asker A] [--limit N] [--before ID]` | The judgement calls dude's models made lately; `--before` pages |
| `judgement ID` | One in full |

### Automations

Work nobody starts by hand: an agent, a command, a server or a webhook, on
a schedule or on GitHub events, the organisation's or a project's
(CONTRIBUTING.md, "Automations"). Anyone reads them; the organisation's
admins change the organisation's, a project's editors the project's; its
owner and the admins transfer one, and its owner may run it.

| Command | Does |
|---|---|
| `automations [PROJECT] [--scope organization\|project] [--trigger schedule\|github] [--kind K] [--enabled true\|false] [--mine] [--name N]` | The organisation's and every project's, filtered (`--kind agent:own`, `agent:reviewer`… for one kind of agent); with PROJECT, its own and the organisation's that also run there |
| `automation ID [--settings]` | One in full; `--settings`: only what `automation-update` takes |
| `automation-create [PROJECT] --name N --kind agent\|command\|server\|webhook --trigger schedule\|github [--preset qa_browser\|reviewer\|investigator] [--tz TZ]` | A new one, as the New automation dialog makes it: **disabled**, owned by you, with defaults (a schedule is weekdays at 09:00 in `--tz`, default UTC; events are `pull_request` opened/synchronize) |
| `automation-update ID SETTINGS` | Replaces its settings with a JSON file, or `-` for stdin. `automation ID`'s output, edited, is accepted as it is: the read-only fields (`id`, `enabled`, `owner`, `lastRun`, …) are dropped |
| `automation-enable ID` / `automation-disable ID` | Turns it on or off (off keeps a kept session) |
| `automation-delete ID`, `automation-duplicate ID` | Deletes it with its runs; copies it, off, as "NAME (copy)" |
| `automation-owner ID PERSON` | Hands it to another member (a person id, from `people`) |
| `automation-secret ID` | A webhook's signing secret, 16–256 characters, read from stdin only, like `login`'s key; only its last four characters are ever shown |
| `automation-run ID` | Runs it now: a schedule as it is |
| `automation-run ID --delivery D` | On GitHub events: with a past delivery (from `automation-deliveries`) |
| `automation-run ID --body FILE [--event E] [--action A] [--delivery-id D]` | On GitHub events: with a test event (`-` reads stdin; the event defaults to the automation's, the delivery id to a made-up `test-…`; `--action` is written into the body, which must name no other action) |
| `automation-runs ID [--status S] [--limit N]` | Its runs, newest first, and its kept session: `queued`, `running`, `succeeded`, `failed`, `skipped`, `cancelled` |
| `automation-run-show ID RUN` | One run: its payloads, log tail, a Server's requests |
| `automation-deliveries ID` | The last 30 days' deliveries of its event |
| `automation-test ID BODY` | Whether an event body would run it: `{"matches": true}`, or the filter that skips it and why |
| `automation-start-fresh ID` | Ends its kept session; the next run starts a new one |
| `automation-prompts ID`, `automation-prompt-restore ID VERSION` | Its prompt's saved versions; saves one (number or id) again as the newest |
| `automation-repos` | Every project's repositories, for an organisation automation's `repoFilter` |
| `automation-next CRON [--tz TZ]` | A schedule's next three times |
| `automation-subscriptions` | The GitHub events the organisation is subscribed to |

### Anything else

`dude api METHOD PATH [JSON | -]` makes any API call with your key:

```sh
dude api GET /v1/navigation
dude api PATCH /v1/projects/prj_… '{"description":"…"}'
dude api POST /v1/forge/credential/verify
```

The routes are in `apps/control-plane/src/api/routes/*.ts`, as
`router.<method>("/v1/…")`, each with a zod schema for its body.

## Examples

Hand work over:

```sh
dude create WEB --title "Retry failed webhook deliveries" --start \
  --criterion "A 5xx delivery is retried with backoff, at most 5 times" \
  --criterion "Tests cover the retry and the give-up path" \
  --goal - <<'EOF'
Webhook deliveries that fail with a 5xx are dropped today
(services/webhooks/send.go). Retry them with exponential backoff…
EOF
```

Follow it, and act when it stops for you:

```sh
dude show WEB-41                          # status; escalation when awaiting_input
dude questions WEB-41                     # what it asks
dude answer q_… "Use the existing queue" --choice 1
dude findings WEB-41 --status open
dude prs WEB-41 | jq -r '.[0].url'
```

Review what it built before it merges:

```sh
dude diff WEB-41 | jq -r '.files[].path'
dude feedback-draft WEB-41 "This retries 4xx too; only 5xx" --path services/webhooks/send.go --lines 40-52
dude feedback-draft WEB-41 "Add a test for the give-up path"
dude feedback-submit WEB-41               # both, to its agents, as one batch
dude feedback WEB-41                      # their state; "blocking" until resolved
```

Take over a half-done branch:

```sh
dude create WEB --repo web --from-branch ana/webhook-retries --base main \
  --title "Finish webhook retries" --goal "…" --start
```

Split work under an epic, in order:

```sh
epic=$(dude epic-create WEB --title "Billing v2" | jq -r .id)
a=$(dude create WEB --epic "$epic" --title "Schema" --goal "…" | jq -r .key)
dude create WEB --epic "$epic" --title "API" --goal "…" --blocked-by "$a"
dude auto-start WEB-43                    # starts once $a merges
```

A weekday-morning agent, from creation to its first run:

```sh
dude automation-next "30 8 * * 1-5" --tz Europe/Lisbon      # check the schedule
id=$(dude automation-create --name "Morning digest" --kind agent --trigger schedule \
  --tz Europe/Lisbon | jq -r .id)                          # disabled, defaults
dude automation "$id" --settings \
  | jq '.trigger.cron = "30 8 * * 1-5"
        | .agent.own.prompt = "Summarise yesterday'\''s merged pull requests."' \
  | dude automation-update "$id" -
dude automation-enable "$id"
dude automation-run "$id"                                   # once now, to see it work
dude automation-runs "$id" --limit 1
```

A command on pull requests, tried on an event before it is turned on:

```sh
id=$(dude automation-create WEB --name "Lint PRs" --kind command --trigger github | jq -r .id)
dude automation "$id" --settings \
  | jq '.command = "npm ci && npm run lint" | .trigger.filters.author = ["!dependabot[bot]"]' \
  | dude automation-update "$id" -
dude automation-test "$id" event.json   # {"matches": false, "filter": "author", "because": "…"}
dude automation-run "$id" --body event.json --action opened   # sets the body's action
dude automation-enable "$id"
```

## Errors

Errors go to stderr as `dude: …`, and the exit status is 1. A refused
request shows the server's answer, as in `dude: GET /v1/tasks/x: 404 {"error":…}`.

- **401**: the key was revoked, or the organisation removed you. Make a new
  key, then `dude login URL`.
- **404 `no route for …`**: the server is older than the command.
- **`not logged in`**: run `dude login URL`, or set `DUDE_API_KEY`.
- **`the key for ORG is gone from the …`**: the keystore lost it, for
  example a new keychain. Log in again.
- **`the server redirected (302) to …`** or **`answered with an HTML
  page`**: something in front of the server (Cloudflare Access, a sign-in
  proxy) wants a browser sign-in. The CLI follows no redirect, so the key
  goes nowhere else. Check the URL (`dude whoami`), or have the proxy let
  requests with an API key through.

## Adding a command

Every authenticated `/v1/` route is either a CLI command or a decision not
to make one. `orchestrator/cmd/dude-cli/routes.json` records which, one
entry per route, keyed `"METHOD /v1/pattern/:param"` as the router
registers it: the command that calls it (a list when several do), or
`{"apiOnly": "<why>"}`.

- **Commands** are the `command` tables in `main.go`, `more.go`,
  `lifecycle.go` and `automations.go`: positional names (`?` optional,
  `...` the rest), one line of help, and a flag set returning a `doer`.
  Reuse `request`/`post`, `onTask`/`onRun` (they resolve TASK and RUN),
  `projectID`, `text` (`-` reads stdin), `obj`, `pick`, `setIf`, and
  `oneOf` to refuse a bad fixed choice before any request.
- **`apps/control-plane/test/cli-routes.test.ts`** (bun, no database)
  fails when a registered route has no entry, an entry names a route that
  is gone, or an `apiOnly` reason is not one of its fixed set. "Not in the
  CLI yet" is closed: only the routes in its `NOT_YET` backlog may use it,
  and a route leaves that backlog when it gets a command.
- **`routes_test.go`** (Go) fails when an entry names a command that does
  not exist, a command is in no entry, a command sends a request its
  entries do not cover (checked on every request in every test, through
  `runCLI`), or a mapping is never exercised: add a row to `routeCases`.
- **Bodies** are written in the row as the route's zod schema takes them,
  read from the handler, never copied from what the CLI sends. Commands
  that build a body from flags also record it: `automations_test.go`
  writes `testdata/automation-requests.json` (`go -C orchestrator test
  ./cmd/dude-cli -run TestAutomationCommandsReachTheirRoutes -update`; a
  normal run fails when it is stale), and
  `apps/control-plane/test/cli-bodies.test.ts` parses each body and query
  with the route's own schema. Follow that pattern for new families.
- **A new POST, PUT or PATCH route that a command calls** must come with its
  body check: the route goes in `bodies` in `cli-bodies.test.ts`, which fails
  for a CLI route with a body that is neither there nor in its shrinking
  `UNCHECKED` backlog. (A new command on a route already in `UNCHECKED` is
  not caught; give it a check while you are there.)
