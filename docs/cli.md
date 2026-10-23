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
2. Log in with the dude's address, and paste the key when asked:

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

```sh
DUDE_URL=https://dude.acme.dev DUDE_API_KEY=$KEY dude list WEB --status review
```

A key kept before `login` existed, as a macOS keychain item named after the
host (`security add-generic-password -s dude.absmartly.dev -w KEY`), is still
used while nothing is logged in. `dude login` replaces it.

## Arguments

- **PROJECT**: a key (`WEB`), slug, name or id.
- **TASK**: a key (`WEB-12`) or id (`wi_…`).
- **RUN**: a run id (`run_…`), or a TASK, meaning its newest run.
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
| `events TASK [--run RUN]` | Event history |
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
| `update TASK [--title] [--goal] [--criterion C]... [--no-criteria] [--epic ID\|none] [--repo R]... [--owner PERSON]` | Changes a task. `--criterion` and `--repo` replace all. |
| `start TASK [--override --note N]` | Delivery: agents implement, review, and open a PR |
| `talk TASK [--override --note N]` | Its conductor plans it with you in Chat; nothing is built yet |
| `auto-start TASK [--how deliver\|talk]` / `no-auto-start TASK` | Starts it once its blockers merge |
| `relate TASK --blocks T \| --blocked-by T \| --related T [--why W]` | Relates two tasks |
| `unrelate TASK blocks\|blocked_by\|related T` | Removes a relation |
| `assign TASK PERSON...` | Who is on it; the first is the owner |
| `decider TASK policy\|conductor [--open-pr]` | Who takes its delivery decisions |
| `retry TASK` | Retries a task whose conductor failed |
| `recover TASK [resume\|retry\|restart] [--note N] [--override]` | A stopped task: without an action, the ways out; with one, takes it |
| `reset-machine TASK ROLE [--note N]` | Throws away its agent machine for ROLE (e.g. `implementer`) |
| `preview TASK [--stop]` | Starts or stops its preview environment |
| `done TASK` | Marks finished work with nothing to merge done |

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
| `reviewers PR LOGIN...` | Requests reviews |
| `repo-decide REQUEST approve\|decline [--note N]` | Settles an agent's request for a repository |

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

Split work under an epic, in order:

```sh
epic=$(dude epic-create WEB --title "Billing v2" | jq -r .id)
a=$(dude create WEB --epic "$epic" --title "Schema" --goal "…" | jq -r .key)
dude create WEB --epic "$epic" --title "API" --goal "…" --blocked-by "$a"
dude auto-start WEB-43                    # starts once $a merges
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
