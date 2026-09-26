# Operations

What a deployment of dude needs: the release, the processes, their
configuration, and the order of an upgrade. Building a release is in the
[README](../README.md#releases).

## The release

A GitHub Release per `v*` tag holds:

| Asset | What it is |
| --- | --- |
| `dude_<version>_linux_arm64.tar.gz`, `…_amd64.tar.gz` | The binaries and their data, below |
| `runtime-image.txt` | One line, `ghcr.io/marcioapm/dude-runtime@sha256:…`: the agent image built from the same commit, for arm64 and amd64 |
| `SHA256SUMS` | `sha256sum` output over the tarballs and `runtime-image.txt` |

Unpack the tarball into a prefix ([layout](../README.md#releases)).
`dude-migrate` resolves its migrations relative to its own path, so a
symlink to it from elsewhere still works.

Agents run the image in `runtime-image.txt`: set `DUDE_AGENT_IMAGE` to it.
A project's own `runtimeImage` overrides it for that project.

## Processes

| Process | Runs as DB role | Listens on | Reachable from |
| --- | --- | --- | --- |
| `dude-backend` | `dude_app` | `PORT` (3000), all interfaces | The internet: people, and GitHub's webhooks at `/v1/webhooks/github/<org>` |
| `dude-orchestrator`, internal API | `dude_app` | `DUDE_ORCHESTRATOR_LISTEN` (127.0.0.1:3100) | The backend only |
| `dude-orchestrator`, agent tools | `dude_app` | `DUDE_TOOLS_LISTEN` (off unless set) | lux runner hosts, where agents' containers run; never the internet |
| `dude-migrate` | the owner | — | — |

The backend runs no background work; the orchestrator runs all of it.
Neither keeps anything on local disk. Run one orchestrator: its loops claim
work through the database, but only one has ever been run at a time.

The orchestrator reaches out to `LUX_URL`, to GitHub's API
(`https://api.github.com`, or the organization's stored `apiBaseUrl`), and
to the push services of browsers that asked for notifications. The backend
reaches out to the orchestrator, and to GitHub's API when a person verifies
a stored credential.

## Postgres

Postgres 17 is what the tests run against. One database, two login roles:

- **The owner** (e.g. `dude`) runs `dude-migrate`, and nothing else. It must
  bypass row-level security (`SUPERUSER` or `BYPASSRLS`): `dude-migrate`
  refuses to apply anything otherwise, since data changes in a migration
  would silently touch no rows. It must be able to create roles: migrations
  create `dude_app` and `dude_sweeper`.
- **`dude_app`** is what `dude-backend` and `dude-orchestrator` connect as.
  It has neither `SUPERUSER` nor `BYPASSRLS`, so row-level security is the
  tenant boundary. Migrations create it with the password `dude_app`: set
  your own (`ALTER ROLE dude_app PASSWORD …`) as the owner, once, after the
  first migration.
- `dude_sweeper` has no login. The orchestrator switches to it
  (`SET LOCAL ROLE`) for sweeps that cross organizations.

The backend holds one connection outside its pool on `LISTEN` for new
events, which is what makes the UI live. Point it at Postgres directly or at
a session-mode pool; `LISTEN` does not work through PgBouncer in transaction
mode.

## Upgrading

1. Verify the tarball against `SHA256SUMS` and unpack it beside the running
   release.
2. Run the new `dude-migrate` with `DATABASE_URL` as the owner. It applies
   what is new, each file in a transaction, and is safe to run again. It
   refuses a migration whose file changed after it was applied.
   `dude-migrate --status` lists applied and pending migrations.
3. Switch to the new release and set `DUDE_AGENT_IMAGE` to the new
   `runtime-image.txt`, then restart `dude-orchestrator` and `dude-backend`.
   A Run that has started keeps its image across resumes.

Between steps 2 and 3 the old processes run against the new schema.

## Environment

**Secret** marks a value that must be kept out of logs and unit files that
others can read.

### dude-orchestrator

| Variable | Default | |
| --- | --- | --- |
| `DATABASE_URL` | required | Postgres, as `dude_app`. **Secret** (password). |
| `DUDE_ORCHESTRATOR_TOKEN` | required | The token the backend authenticates with; also signs agents' tool tokens unless `DUDE_TOOLS_KEY` is set. **Secret.** |
| `DUDE_ORCHESTRATOR_LISTEN` | `127.0.0.1:3100` | Internal API address. |
| `LUX_URL` | required | The lux control plane. |
| `LUX_API_KEY` | required | A lux API key with the `run` scope. **Secret.** |
| `DUDE_AGENT_IMAGE` | `localhost/dude-runtime:dev` | Image for agents when a project names none: the release's `runtime-image.txt`. |
| `DUDE_OPENCODE_AUTH` | `~/.local/share/opencode/auth.json` | OpenCode's `auth.json`: a path to it, or its contents. Given to agents as a file secret. **Secret.** |
| `DUDE_OPENCODE_CONFIG` | `~/.config/opencode/opencode.json` | OpenCode's config, path or contents; only its `provider` object is used. Its providers' `baseURL` hosts become agents' allowed egress. **Secret** if it holds keys. |
| `DUDE_AGENT_EGRESS` | none | Comma-separated hosts agents may reach besides their model provider; `*` turns egress filtering off. With neither this nor a provider `baseURL`, egress is unrestricted. |
| `DUDE_AGENT_TIMEOUT` | none | A limit on a Run's running time, passed to lux. |
| `DUDE_TOOLS_LISTEN` | off | Address the agent tools listen on, e.g. `0.0.0.0:3200`. Unset, agents get no dude tools. |
| `DUDE_TOOLS_URL` | none | The tools as agents' containers reach them, e.g. `http://10.0.1.5:3200`. Unset, agents get no dude tools. Must not be the lux host or lux's own address: lux never lets a Run reach either. |
| `DUDE_TOOLS_SERVICE` | on | `off` for a lux without workload services: the agent is then handed the tool token directly. |
| `DUDE_TOOLS_KEY` | `DUDE_ORCHESTRATOR_TOKEN` | Key that derives each Run's tool token. Changing it invalidates live Runs' tokens. **Secret.** |
| `DUDE_PR_RECONCILE` | `15m` | How often open pull requests are re-read as a backstop to webhooks. |
| `DUDE_PARK_AFTER` | the delivery policy's | Grace before a Run waiting on a person is parked, for projects that set none (a Go duration). |
| `DUDE_IDLE_AFTER` | the delivery policy's | Quiet time before an idle Run is nudged and parked, for projects that set none. |
| `DUDE_VAPID_PUBLIC_KEY`, `DUDE_VAPID_PRIVATE_KEY` | made once, kept in `push_config` | Web Push keys. The private key is a **secret**. Changing them invalidates existing browser subscriptions. |
| `DUDE_VAPID_SUBJECT` | `mailto:dude@localhost` | Who push services may contact (`mailto:` or `https:`). |
| `DUDE_FACTORY_LOGINS` | none | Comma-separated GitHub logins whose PR comments are the factory's own, and wake no agent. |
| `HOME` | the service user's | Where the OpenCode defaults above are read from, when not set explicitly. |

### dude-backend

| Variable | Default | |
| --- | --- | --- |
| `DATABASE_URL` | required | Postgres, as `dude_app`. **Secret** (password). |
| `DUDE_ORCHESTRATOR_URL` | none | The orchestrator's internal API, e.g. `http://127.0.0.1:3100`. Unset, anything that changes what runs answers 503. |
| `DUDE_ORCHESTRATOR_TOKEN` | none | The same token as the orchestrator's. **Secret.** |
| `PORT` | `3000` | Listening port, on all interfaces. |
| `DUDE_WEB_DIR` | off | Serve the web app from this directory: `<prefix>/share/dude/web`. Unset, the backend serves only the API. |

With `DUDE_WEB_DIR` set, GET and HEAD requests that match no API route and
are outside `/v1` and `/health` are served from the directory, and unknown
paths get `index.html`. Files under `/assets/` are cached for a year;
everything else is revalidated.

### dude-migrate

| Variable | Default | |
| --- | --- | --- |
| `DATABASE_URL` | required | Postgres, as the owner. **Secret.** |
| `DUDE_MIGRATIONS_DIR` | `../share/dude/migrations` beside the binary | The directory of `.sql` files. |

### Stored in the database, not configured

Organizations' GitHub credentials and webhook secrets (entered through the
API, stored in plaintext today), people's API keys (hashed), and the VAPID
keys when they are not configured.

## Health

`GET /health` on the backend, and on the orchestrator's internal API,
answers 200 `{"status":"ok"}` when it can reach the database, 503
otherwise. The orchestrator logs to stderr (`log/slog` text); a loop that
fails logs `loop failed` and keeps running.
