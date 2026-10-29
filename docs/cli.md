# The dude CLI

`dude` drives a dude server from your terminal, as you: it records tasks,
starts them, answers agents' questions, steers runs, merges pull requests,
and reaches any route of the HTTP API. Output is indented JSON, for people
and scripts alike (`| jq`).

It is not the `dude` inside an agent's container (`orchestrator/cmd/dude`),
which acts as its Run and has other commands. This one is
`orchestrator/cmd/dude-cli`, installed under the name `dude`.

Commands are a noun and a verb: `dude task update WEB-12 --title …`,
`dude automation show aut_…`. The flat names of earlier releases
(`dude update`, `dude automation-update`, `dude prs`) were removed in this
one, with no aliases; [Old names](#old-names) maps each to its new name.

## Install

On macOS or Linux (arm64 or amd64):

```sh
curl -fsSL https://raw.githubusercontent.com/marcioapm/dude/main/scripts/install-cli.sh | sh
```

It downloads the latest release's `dude-cli_<version>_<os>_<arch>.tar.gz`
from GitHub, checks it against the release's `SHA256SUMS`, and installs
`bin/dude` to `~/.local/bin`, saying what to add to `PATH` when that is not
on it. `DUDE_VERSION=v1.2.3` installs that release; `DUDE_INSTALL_DIR`
installs somewhere else. (`DUDE_RELEASES_URL` replaces GitHub's API, for
tests only.)

Upgrade it later with the CLI itself (`upgrade` is a top-level command):

```sh
dude upgrade --check              # {"current", "latest", "updateAvailable"}; changes nothing
dude upgrade                      # to the latest release
dude upgrade --version v1.2.3     # to that release, newer or older
```

`dude upgrade` downloads and verifies the same way, then writes the new
binary next to the running one and renames it over it. It refuses when it
cannot write there (installed by a package manager, or to a system
directory): upgrade it the way it was installed, or re-run with permission.
A build from a checkout (`dude --version` says `dev`) is replaced only with
an explicit `--version`. On Windows it refuses; download the release by
hand.

By hand, from a release, unpack the tarball and put `bin/dude` on your `PATH`:

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
dude --org northwind task list WEB      # in Northwind, this once
DUDE_ORG=northwind dude task list WEB   # the same, for a script
dude whoami                             # account, organisation, role
dude logout northwind                   # forget its key
```

`--org` goes before the command, noun and all.

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
DUDE_URL=https://dude.acme.dev DUDE_API_KEY=$KEY dude task list WEB --status review
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
  commands that list them (`epic list`, `finding list`, `pr list`,
  `question list`, `repo requests`).
- A text argument of `-` reads stdin. Use a heredoc for anything long.

`dude --help` lists the top-level commands and the nouns. `dude NOUN` (or
`dude NOUN --help`, or `dude help NOUN`) lists a noun's verbs; `dude NOUN
VERB --help` (or `dude help NOUN VERB`) gives one's options. `dude
--version` gives the version.

## Commands

A few commands stand alone; every other one is `dude NOUN VERB`, where the
noun is what it acts on.

### Top level

| Command | Does |
|---|---|
| `login URL [--file]` | Checks a key, then keeps it for its organisation |
| `logout [ORG]` | Forgets an organisation's key (default: the current one) |
| `orgs` | The organisations logged in to |
| `use ORG` | Switches the current organisation |
| `whoami` | The account, organisation and role in use |
| `upgrade [--version vX.Y.Z] [--check]` | Replaces this binary with a GitHub release of the CLI (see [Install](#install)); `--check` only compares versions |
| `guide [PAGE]` | dude's guide: its pages, or one in full (the same text agents search). `dude --help` is this CLI's help. |
| `api METHOD PATH [JSON \| -]` | Any API call; see [Anything else](#anything-else) |
| `version`, `help` | Also `--version`, `--help` |

### task

| Command | Does |
|---|---|
| `task list PROJECT [--status S]` | A project's tasks: `received`, `queued`, `running`, `awaiting_input`, `review`, `ready_to_merge`, `done`, `failed`, `aborted` |
| `task show TASK` | Status, why it waits (escalation), goal, criteria, runs |
| `task raw TASK` | The task as the API returns it |
| `task relations TASK` | What blocks it, what it blocks, what it relates to |
| `task metrics TASK` | Cost and time |
| `task events TASK [--run RUN] [--after N] [--limit N]` | Event history; pass `nextCursor` as `--after` for the next page |
| `task artifacts TASK` | Files its agents published |
| `task servers TASK` | Its dev servers and preview |
| `task verification TASK` | How its work was verified, criterion by criterion |
| `task create PROJECT --title T --goal G [--criterion C]... [--repo NAME[:read]]... [--epic ID] [--blocked-by T]... [--blocks T]... [--related T]... [--start \| --talk]` | Records a task. The goal needs 16+ characters. `--repo` is needed when the project has more than one repository. |
| `task create … --repo NAME --from-pr N [--mode continue\|review]` / `--from-branch B [--base B]` | A task that takes over existing work in that repository: a pull request (number or URL) or a branch. `review` only reviews it. |
| `task update TASK [--title] [--goal] [--criterion C]... [--no-criteria] [--epic ID\|none] [--repo R]... [--owner PERSON]` | Changes a task. `--criterion` and `--repo` replace all. |
| `task start TASK [--override --note N]` | Delivery: agents implement, review, and open a PR |
| `task talk TASK [--override --note N]` | Its conductor plans it with you in Chat; nothing is built yet |
| `task chat TASK TEXT [--aside]` | Tells the task's conductor; `--aside` is for people only |
| `task decide TASK ACTION [--note N]` | Your decision when delivery stopped for you; `task show` lists the actions |
| `task auto-start TASK [--how deliver\|talk]` / `task no-auto-start TASK` | Starts it once its blockers merge |
| `task relate TASK --blocks T \| --blocked-by T \| --related T [--why W]` | Relates two tasks |
| `task unrelate TASK blocks\|blocked_by\|related T` | Removes a relation |
| `task assign TASK PERSON...` | Who is on it; the first is the owner |
| `task decider TASK policy\|conductor [--open-pr] [--draft]` | Who takes its delivery decisions; `--draft` opens the pull request as a draft |
| `task retry TASK` | Retries a task whose conductor failed |
| `task recover TASK [resume\|retry\|restart] [--note N] [--override]` | A stopped task: without an action, the ways out; with one, takes it |
| `task reset-machine TASK ROLE [--note N]` | Throws away its agent machine for ROLE (e.g. `implementer`) |
| `task preview TASK [--stop]` | Starts or stops its preview environment |
| `task done TASK` | Marks finished work with nothing to merge done |
| `task before-pr TASK open\|draft\|ask\|project` | What Deliver does before its first pull request: open it, open a draft, or ask its owner first; `project` follows the project's setting. Its owner only, until a pull request is open |
| `task done-without-pr TASK` | Answers a task held before its pull request: Done, its branch kept on GitHub, no pull request. Its owner only |
| `task stop TASK [--restart [--decider policy\|conductor]] [--note N]` | Stops all of its work where it stands; `--restart` starts its next attempt |
| `task close TASK wont_do\|duplicate\|cant_reproduce [--duplicate-of T] [--close-pr] [--remove-relations] [--note N]` | Ends it without doing it |
| `task reopen TASK` | Reopens a closed task |
| `task archive TASK [--resolution R] [--duplicate-of T] [--note N]` / `task unarchive TASK` | Archives it (one not ended needs `--resolution`, and is closed with it), or brings it back |

### run

| Command | Does |
|---|---|
| `run show RUN` | One run in full |
| `run diff RUN` | What a run changed |
| `run directives RUN` | What people told a run |
| `run steer RUN TEXT [--interrupt]` | A running agent reads it at its next step; `--interrupt` discards its current turn |
| `run pause RUN [--hard] [--reason R]`, `run resume RUN [--reason R]`, `run abort RUN [--reason R]` | Pauses, resumes or ends a run |
| `run restart RUN NOTE [--tier T]` | A fresh run in its slot, told what to do differently |
| `run leave RUN` | Leaves a stalled run as it is |

### session

| Command | Does |
|---|---|
| `session show ID` | An agent session and its children |

### question

| Command | Does |
|---|---|
| `question list [TASK] [--run RUN]` | Agents' questions; with neither argument, every open one |
| `question answer QUESTION [TEXT] [--choice N]...` | Answers an agent's question |

### finding

| Command | Does |
|---|---|
| `finding list [TASK] [--run RUN] [--status open]` | Review findings |
| `finding resolve FINDING resolved\|accepted\|open [--note N]` | Settles a finding; `accepted` leaves it as it is |

### review-question

Questions reviewers asked a task's owner; they hold delivery until settled.

| Command | Does |
|---|---|
| `review-question list TASK` | The task's reviewers' questions |
| `review-question answer QUESTION TEXT` | Answers one (the task's owner) |
| `review-question withdraw QUESTION [--reason R]` | Withdraws one unanswered (the task's owner) |

### pr

| Command | Does |
|---|---|
| `pr list TASK` | A task's pull requests |
| `pr merge PR [--method merge\|squash\|rebase]` | Merges (default: the organisation's method) |
| `pr update-branch PR` | Brings its branch up to date with its base |
| `pr rerun-checks PR` | Reruns its failed checks |
| `pr resolve-conflicts PR` | Has an agent resolve its merge conflicts |
| `pr reviewers PR LOGIN...` | Requests reviews |

### feedback

Feedback is your own review of what the agents built. Items start as
drafts, which only you see. Submitting sends them to the task's agents in
one batch. Open items block the pull request and dude's merge until they
are resolved.

| Command | Does |
|---|---|
| `feedback list TASK [--attempt N]` | Everyone's submitted items, your drafts, and how many block |
| `feedback draft TASK TEXT` | A draft about the work in general |
| `feedback draft TASK TEXT --path FILE --lines N[-M] [--side new\|old] [--run RUN]` | A draft on lines of a run's diff (default: the task's newest run, the new side) |
| `feedback submit TASK [DRAFT,…]` | Sends drafts: the ones named, or all of yours |
| `feedback send TASK TEXT` | Drafts one general item and sends it at once |
| `feedback reply ITEM TEXT` | Replies on an item |
| `feedback resolve ITEM [--note N]` / `feedback reopen ITEM [TEXT]` | Resolves an item, or reopens it, saying why |
| `feedback delete DRAFT` | Deletes one of your drafts |

### project

| Command | Does |
|---|---|
| `project list` | The projects you can see |
| `project overview PROJECT` | A project's board, by epic |
| `project create --name N --slug s [--key K] [--description D] [--repo NAME=URL]...` | A new project (admins) |

### repo

| Command | Does |
|---|---|
| `repo list PROJECT` | A project's repositories |
| `repo add PROJECT --name N --url URL [--branch main]` | Adds a repository |
| `repo requests [TASK]` | Agents' requests for another repository |
| `repo decide REQUEST approve\|decline [--note N]` | Settles an agent's request for a repository |

### preview

A preview of a project's branch, with no task: every repository of the
project at that branch where it has it, else its default branch, on the
servers marked to start in previews. It syncs on every push to the branch,
and ends when the branch is deleted or after 30 days unused, independently
of the task-preview setting `previews.reap_after`. Opening one
of its URLs wakes it; without a browser, `dude preview start ID` does.

| Command | Does |
|---|---|
| `preview create PROJECT BRANCH [--start [--wait DURATION]]` | Creates or reuses the live preview and registers its server URLs, leaving a new preview asleep. `--start` also wakes all autostart preview servers. Refused when the project's first repository has no such branch |
| `preview start ID [--wait DURATION]` / `preview start PROJECT BRANCH [--wait DURATION]` | Wakes an existing live preview, never creates one; missing previews fail with `not_found` |
| `preview list PROJECT` | The project's live branch previews: branch, servers and URLs, status, last used, started by |
| `preview show ID` | One branch preview, live or ended |
| `preview stop ID` / `preview stop PROJECT BRANCH` | Ends it: its servers are deleted |

`--wait` defaults to `0`: request startup without waiting for readiness.
A positive duration such as `--wait 2m` waits for **every autostart preview
recipe's live server process** to report `ready` through dude's authenticated
run-servers API. Allocated URLs and a `running` Run are not readiness;
unrelated dynamic servers do not block it. No anonymous preview URL requests
are made (including through Cloudflare Access). Negative durations are errors,
and `preview create --wait` requires `--start`, even with `--wait 0`.

Creation waits up to 30 seconds for URL allocation without waking anything.
Startup also polls for missing server allocation (at most 30 seconds); a
positive `--wait` bounds the entire command, including resolution, allocation,
startup requests and readiness. Without readiness waiting the command has a
30-second orchestration bound. Output is a JSON snapshot with preview ID,
URLs and status; startup includes live `run`/server state and an explicit
`ready` boolean, not a promise of readiness. Failures and timeouts exit nonzero
and identify the preview and latest observed state where available.

Starting by ID addresses that exact preview; PROJECT BRANCH resolves only an
existing live one and never creates it. Starting an already-ready preview is
safe. `stop` **ends/deletes**, not parks: a later `create` makes a new preview.
There is no compatibility alias for the former create-like `preview start`.

### epic

| Command | Does |
|---|---|
| `epic list PROJECT` | A project's epics |
| `epic create PROJECT --title T [--description D]` | A new epic |
| `epic update EPIC [--title] [--description] [--state planned\|active\|done\|none] [--position N]` | Changes an epic |
| `epic delete EPIC` | Deletes an epic; its tasks stay in the project |
| `epic archive EPIC [--ended-only]` / `epic unarchive EPIC` | Archives an epic with its tasks (`--ended-only`: only its ended ones), or brings it back |
| `epic metrics EPIC` | An epic's cost and time |
| `epic peers EPIC` | What each task on it is doing |
| `epic threads EPIC` | Conversations between its agents |

### note

Notes pinned on an epic, which every agent on it reads.

| Command | Does |
|---|---|
| `note list EPIC` | The epic's notes |
| `note add EPIC TEXT [--replaces N]` | Pins a note |
| `note retire EPIC N` | Unpins note N |

### brainstorm

A brainstorm is an agent to think something through with; it writes no code.

| Command | Does |
|---|---|
| `brainstorm list [--archived]` | Your brainstorms |
| `brainstorm create MESSAGE [--title T] [--project P]... [--tier T]` | Starts one |
| `brainstorm show ID` | One in full |
| `brainstorm chat ID TEXT` | Says something |
| `brainstorm answer ID QUESTION [TEXT] [--choice N]...` | Answers its question |
| `brainstorm file ID PROPOSAL ITEM...` | Files a proposal's items (by index) as tasks |
| `brainstorm invite ID PERSON... [--role chat\|read]` | Invites people |
| `brainstorm rename ID TITLE`, `brainstorm retry ID`, `brainstorm archive ID`, `brainstorm unarchive ID` | Renames, retries, archives, unarchives |

### memory

| Command | Does |
|---|---|
| `memory search QUERY [--project P] [--type T,…] [--limit N] [--words]` | Searches what agents and people remembered (`--words`: by word, not meaning) |
| `memory list [--project P] [--text T] [--archived]` | Lists memories |
| `memory show ID` | One in full |
| `memory add --title T --content C [--kind fact\|procedure\|note] [--project P]` | Remembers something |
| `memory archive ID`, `memory unarchive ID` | Archives or unarchives |

### automation

Work nobody starts by hand: an agent, a command, a server or a webhook, on
a schedule or on GitHub events, the organisation's or a project's
(CONTRIBUTING.md, "Automations"). Anyone reads them; the organisation's
admins change the organisation's, a project's editors the project's; its
owner and the admins transfer one, and its owner may run it.

| Command | Does |
|---|---|
| `automation list [PROJECT] [--scope organization\|project] [--trigger schedule\|github\|incoming] [--kind K] [--enabled true\|false] [--mine] [--name N]` | The organisation's and every project's, filtered (`--kind agent:own`, `agent:reviewer`… for one kind of agent); with PROJECT, its own and the organisation's that also run there |
| `automation show ID [--settings]` | One in full; `--settings`: only what `automation update` takes |
| `automation create [PROJECT] --name N --kind agent\|command\|server\|webhook --trigger schedule\|github\|incoming [--preset qa_browser\|reviewer\|investigator] [--tz TZ]` | A new one, as the New automation dialog makes it: **disabled**, owned by you, with defaults (a schedule is weekdays at 09:00 in `--tz`, default UTC; events are `pull_request` opened/synchronize; an incoming webhook verifies by signature, and its secret is in the answer's `incomingSecret`, this once) |
| `automation update ID SETTINGS` | Replaces its settings with a JSON file, or `-` for stdin. `automation show ID`'s output, edited, is accepted as it is: the read-only fields (`id`, `enabled`, `owner`, `lastRun`, …) are dropped |
| `automation enable ID` / `automation disable ID` | Turns it on or off (off keeps a kept session) |
| `automation delete ID`, `automation duplicate ID` | Deletes it with its runs; copies it, off, as "NAME (copy)" |
| `automation owner ID PERSON` | Hands it to another member (a person id, from `person list`) |
| `automation secret ID` | A webhook's signing secret, 16–256 characters, read from stdin only, like `login`'s key; only its last four characters are ever shown |
| `automation run ID` | Runs it now: a schedule as it is |
| `automation run ID --delivery D` | On GitHub events: with a past delivery (from `automation deliveries`) |
| `automation run ID --body FILE [--event E] [--action A] [--delivery-id D]` | On GitHub events: with a test event (`-` reads stdin; the event defaults to the automation's, the delivery id to a made-up `test-…`; `--action` is written into the body, which must name no other action). On an incoming webhook: `--body` alone, the request body to run with |
| `automation run-list ID [--status S] [--limit N]` | Its runs, newest first, and its kept session: `queued`, `running`, `succeeded`, `failed`, `skipped`, `cancelled` |
| `automation run-show ID RUN` | One run: its payloads, log tail, a Server's requests, an incoming run's deliveries |
| `automation deliveries ID` | The last 30 days' deliveries of its event, or to its incoming URL, each with what dude did (`verdict`, `reason`) |
| `automation delivery ID DELIVERY` | One delivery: verdict, group key, headers (secrets as `••••`), body |
| `automation rotate ID` | A new verification secret for an incoming webhook, printed this once (`incomingSecret`); the old one stops working |
| `automation test ID BODY` | Whether an event body would run it: `{"matches": true}`, or the filter that skips it and why |
| `automation start-fresh ID` | Ends its kept session; the next run starts a new one |
| `automation prompts ID`, `automation prompt-restore ID VERSION` | Its prompt's saved versions; saves one (number or id) again as the newest |
| `automation repos` | Every project's repositories, for an organisation automation's `repoFilter` |
| `automation next CRON [--tz TZ]` | A schedule's next three times |
| `automation subscriptions` | The GitHub events the organisation is subscribed to |

### person

| Command | Does |
|---|---|
| `person list` | The organisation's people |
| `person invite EMAIL... [--role member\|admin]` | Invites people (admins) |

### judgement

| Command | Does |
|---|---|
| `judgement list [--asker A] [--limit N] [--before ID]` | The judgement calls dude's models made lately; `--before` pages |
| `judgement show ID` | One in full |

### Anything else

`dude api METHOD PATH [JSON | -]` makes any API call with your key:

```sh
dude api GET /v1/navigation
dude api PATCH /v1/projects/prj_… '{"description":"…"}'
dude api POST /v1/forge/credential/verify
```

The routes are in `apps/control-plane/src/api/routes/*.ts`, as
`router.<method>("/v1/…")`, each with a zod schema for its body.

### Old names

Each old flat name is now an unknown command; its arguments and flags are
the same under the new name.

| Old | New | Old | New |
|---|---|---|---|
| `people` | `person list` | `invite` | `person invite` |
| `projects` | `project list` | `overview` | `project overview` |
| `project-create` | `project create` | `repos` | `repo list` |
| `repo-add` | `repo add` | `repo-requests` | `repo requests` |
| `repo-decide` | `repo decide` | `epics` | `epic list` |
| `epic-create` | `epic create` | `epic-update` | `epic update` |
| `epic-delete` | `epic delete` | `epic-archive` | `epic archive` |
| `epic-unarchive` | `epic unarchive` | `epic-metrics` | `epic metrics` |
| `peers` | `epic peers` | `threads` | `epic threads` |
| `notes` | `note list` | `note` | `note add` |
| `note-retire` | `note retire` | `list` | `task list` |
| `show` | `task show` | `raw` | `task raw` |
| `relations` | `task relations` | `metrics` | `task metrics` |
| `events` | `task events` | `artifacts` | `task artifacts` |
| `servers` | `task servers` | `verification` | `task verification` |
| `create` | `task create` | `update` | `task update` |
| `start` | `task start` | `talk` | `task talk` |
| `auto-start` | `task auto-start` | `no-auto-start` | `task no-auto-start` |
| `relate` | `task relate` | `unrelate` | `task unrelate` |
| `assign` | `task assign` | `decider` | `task decider` |
| `retry` | `task retry` | `recover` | `task recover` |
| `reset-machine` | `task reset-machine` | `preview` | `task preview` (`preview` is now a branch preview's noun) |
| `done` | `task done` | `stop` | `task stop` |
| `close` | `task close` | `reopen` | `task reopen` |
| `archive` | `task archive` | `unarchive` | `task unarchive` |
| `chat` | `task chat` | `decide` | `task decide` |
| `run` | `run show` | `diff` | `run diff` |
| `directives` | `run directives` | `steer` | `run steer` |
| `pause` | `run pause` | `resume` | `run resume` |
| `abort` | `run abort` | `restart` | `run restart` |
| `leave` | `run leave` | `session` | `session show` |
| `questions` | `question list` | `answer` | `question answer` |
| `findings` | `finding list` | `resolve` | `finding resolve` |
| `review-questions` | `review-question list` | `review-answer` | `review-question answer` |
| `review-withdraw` | `review-question withdraw` | `prs` | `pr list` |
| `merge` | `pr merge` | `update-branch` | `pr update-branch` |
| `rerun-checks` | `pr rerun-checks` | `resolve-conflicts` | `pr resolve-conflicts` |
| `reviewers` | `pr reviewers` | `feedback` | `feedback list` |
| `feedback-draft` | `feedback draft` | `feedback-submit` | `feedback submit` |
| `feedback-send` | `feedback send` | `feedback-reply` | `feedback reply` |
| `feedback-resolve` | `feedback resolve` | `feedback-reopen` | `feedback reopen` |
| `feedback-delete` | `feedback delete` | `brainstorms` | `brainstorm list` |
| `brainstorm` | `brainstorm create` | `brainstorm-show` | `brainstorm show` |
| `brainstorm-chat` | `brainstorm chat` | `brainstorm-answer` | `brainstorm answer` |
| `brainstorm-file` | `brainstorm file` | `brainstorm-invite` | `brainstorm invite` |
| `brainstorm-title` | `brainstorm rename` | `brainstorm-retry` | `brainstorm retry` |
| `brainstorm-archive` | `brainstorm archive` | `brainstorm-unarchive` | `brainstorm unarchive` |
| `memory-search` | `memory search` | `memories` | `memory list` |
| `memory` | `memory show` | `memory-add` | `memory add` |
| `memory-archive` | `memory archive` | `memory-restore` | `memory unarchive` |
| `judgements` | `judgement list` | `judgement` | `judgement show` |
| `automations` | `automation list` | `automation ID` | `automation show ID` |
| `automation-create` | `automation create` | `automation-update` | `automation update` |
| `automation-enable` | `automation enable` | `automation-disable` | `automation disable` |
| `automation-delete` | `automation delete` | `automation-duplicate` | `automation duplicate` |
| `automation-owner` | `automation owner` | `automation-secret` | `automation secret` |
| `automation-run` | `automation run` | `automation-runs` | `automation run-list` |
| `automation-run-show` | `automation run-show` | `automation-deliveries` | `automation deliveries` |
| `automation-test` | `automation test` | `automation-start-fresh` | `automation start-fresh` |
| `automation-prompts` | `automation prompts` | `automation-prompt-restore` | `automation prompt-restore` |
| `automation-repos` | `automation repos` | `automation-next` | `automation next` |
| `automation-subscriptions` | `automation subscriptions` | | |

`login`, `logout`, `use`, `orgs`, `whoami`, `upgrade`, `guide`, `api` and
`version` are unchanged.

## Examples

Hand work over:

```sh
dude task create WEB --title "Retry failed webhook deliveries" --start \
  --criterion "A 5xx delivery is retried with backoff, at most 5 times" \
  --criterion "Tests cover the retry and the give-up path" \
  --goal - <<'EOF'
Webhook deliveries that fail with a 5xx are dropped today
(services/webhooks/send.go). Retry them with exponential backoff…
EOF
```

Follow it, and act when it stops for you:

```sh
dude task show WEB-41                     # status; escalation when awaiting_input
dude question list WEB-41                 # what it asks
dude question answer q_… "Use the existing queue" --choice 1
dude finding list WEB-41 --status open
dude pr list WEB-41 | jq -r '.[0].url'
```

Review what it built before it merges:

```sh
dude run diff WEB-41 | jq -r '.files[].path'
dude feedback draft WEB-41 "This retries 4xx too; only 5xx" --path services/webhooks/send.go --lines 40-52
dude feedback draft WEB-41 "Add a test for the give-up path"
dude feedback submit WEB-41               # both, to its agents, as one batch
dude feedback list WEB-41                 # their state; "blocking" until resolved
```

Take over a half-done branch:

```sh
dude task create WEB --repo web --from-branch ana/webhook-retries --base main \
  --title "Finish webhook retries" --goal "…" --start
```

Split work under an epic, in order:

```sh
epic=$(dude epic create WEB --title "Billing v2" | jq -r .id)
a=$(dude task create WEB --epic "$epic" --title "Schema" --goal "…" | jq -r .key)
dude task create WEB --epic "$epic" --title "API" --goal "…" --blocked-by "$a"
dude task auto-start WEB-43               # starts once $a merges
```

A weekday-morning agent, from creation to its first run:

```sh
dude automation next "30 8 * * 1-5" --tz Europe/Lisbon      # check the schedule
id=$(dude automation create --name "Morning digest" --kind agent --trigger schedule \
  --tz Europe/Lisbon | jq -r .id)                          # disabled, defaults
dude automation show "$id" --settings \
  | jq '.trigger.cron = "30 8 * * 1-5"
        | .agent.own.prompt = "Summarise yesterday'\''s merged pull requests."' \
  | dude automation update "$id" -
dude automation enable "$id"
dude automation run "$id"                                   # once now, to see it work
dude automation run-list "$id" --limit 1
```

A command on pull requests, tried on an event before it is turned on:

```sh
id=$(dude automation create WEB --name "Lint PRs" --kind command --trigger github | jq -r .id)
dude automation show "$id" --settings \
  | jq '.command = "npm ci && npm run lint" | .trigger.filters.author = ["!dependabot[bot]"]' \
  | dude automation update "$id" -
dude automation test "$id" event.json   # {"matches": false, "filter": "author", "because": "…"}
dude automation run "$id" --body event.json --action opened   # sets the body's action
dude automation enable "$id"
```

## Errors

Errors go to stderr as `dude: …`, and the exit status is 1. A refused
request shows the server's answer, as in `dude: GET /v1/tasks/x: 404 {"error":…}`.

- **`unknown command "x"; see dude --help`**: `x` is neither a top-level
  command nor a noun; an old flat name such as `automation-update` is one
  (see [Old names](#old-names)).
- **`dude task has no "x"; its commands: …`**: the noun has no such verb.
- **`usage: dude task update TASK`**: the wrong number of arguments.
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
registers it: the command that calls it, by its full name (`"task update"`;
a list when several do), or `{"apiOnly": "<why>"}`.

- **Name it `NOUN VERB`.** The noun is the resource it acts on: the first
  resource segment of the route it calls, in the singular (`/v1/tasks/…` →
  `task`, `/v1/pull-requests/…` → `pr`, `/v1/repositories/…` → `repo`,
  `/v1/people` → `person`). A route nested under a task that acts on
  another resource takes that resource's noun
  (`/v1/tasks/:id/review-questions` → `review-question list`). A noun of
  two words is hyphenated and still counts as one word. Reuse an existing
  noun before adding one: a noun with one or two verbs folds into its
  parent when that reads naturally. The verb is `list`, `show`, `create`,
  `update`, `delete`, `archive`, `unarchive`, `enable`, `disable`,
  `restore` or `search`, or the product's own word for the action (`task
  start`, `run steer`, `pr merge`, `feedback submit`). A list is
  `NOUN list`, never a plural noun. A sub-resource is one hyphenated verb
  (`automation run-show`), never a third word. Only people- and
  setup-level commands with no resource stay top-level (`login`, `use`,
  `upgrade`, `api`, `guide`).
- **Commands** are the `command` tables in `main.go`, `more.go`,
  `lifecycle.go` and `automations.go`, keyed by the full name
  (`"automation update"`): positional names (`?` optional, `...` the
  rest), one line of help, and a flag set returning a `doer`. A new noun
  also goes in `nouns` in `main.go`, with one line for `dude --help`; a
  test fails when a command's noun is missing there. Name the command in
  its error strings as the user types it (`"task update: nothing to
  change"`). Reuse `request`/`post`, `onTask`/`onRun` (they resolve TASK
  and RUN), `projectID`, `text` (`-` reads stdin), `obj`, `pick`, `setIf`,
  and `oneOf` to refuse a bad fixed choice before any request.
- **`apps/control-plane/test/cli-routes.test.ts`** (bun, no database)
  fails when a registered route has no entry, an entry names a route that
  is gone, or an `apiOnly` reason is not one of its fixed set. "Not in the
  CLI yet" is closed: only the routes in its `NOT_YET` backlog may use it,
  and a route leaves that backlog when it gets a command.
- **`routes_test.go`** (Go) fails when an entry names a command that does
  not exist, a command is in no entry, a command sends a request its
  entries do not cover (checked on every request in every test, through
  `runCLI`), or a mapping is never exercised: add a row to `routeCases`,
  whose `args` are the command line as typed (`{"task", "update", …}`).
  `localCommands` lists the top-level commands that call no dude route.
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
