# Operations

What a deployment of dude needs: the release, the processes, their
configuration, and the order of an upgrade. Building a release is in the
[README](../README.md#releases).

## GitHub credentials

Before running tasks against GitHub, configure the organization’s token
with the permissions for the operations it should perform. See
[GitHub credentials and permissions](github.md) for fine-grained and classic
PAT checklists, workflow-file access, CI reruns, webhooks and troubleshooting.
A successful connection or clone does not prove push/workflow access.

## The release

A GitHub Release per `v*` tag holds:

| Asset | What it is |
| --- | --- |
| `dude_<version>_linux_arm64.tar.gz`, `…_amd64.tar.gz` | The binaries and their data, below |
| `SHA256SUMS` | `sha256sum` output over the tarballs |

Unpack the tarball into a prefix ([layout](../README.md#releases)).
`dude-migrate` carries its migrations inside itself; it reads no SQL from
disk.

### Release archive

At the archive's root, beside `bin/` and `share/`, `FEATURES` lists what
the release supports, one word per line. A deployment tool reads it from
the archive before it relies on anything an older release lacks: a release
without the file, or without the word, does not support the feature.

| Feature | What it promises |
| --- | --- |
| `validate` | `dude-orchestrator validate` and `dude-backend validate` exist ([Validating a configuration](#validating-a-configuration)). Check it first: an older `dude-orchestrator` ignores the argument and **starts the service**. |
| `image-builder` | `bin/dude-image-builder` exists, migration 068 creates the `dude_builder` role it connects as, and the `[images]` and `[builder]` settings are known ([design](design/images.md)). Run the builder only for a release that declares it. It exits non-zero at start unless `podman info` reports the `cpu` and `memory` cgroup controllers for its user, and writes a heartbeat every 30 s that the Images page reads. |

A release holds no agent image. `DUDE_AGENT_IMAGE` is the operator's own:
any registry lux's runners can pull from, pinned by digest
(`registry.example/agents@sha256:…`). A project's own `runtimeImage`
overrides it for that project. [`images/runtime/Dockerfile`](../images/runtime/Dockerfile)
is a starting point; it needs the release's `dude` CLI, which
`scripts/runtime-image.sh` builds before a local `docker build`.

### Private agent images

lux logs its runners in to a registry per Run (lux's `docs/runspec.md`,
"Private registries"). `DUDE_REGISTRY_AUTH` says where dude gets that
login:

- `none` (the default): no login; the image must be pullable as is.
- `ecr`: `DUDE_AGENT_IMAGE` must be in ECR
  (`<account>.dkr.ecr.<region>.amazonaws.com/…`), or the orchestrator
  refuses to start. It calls `ecr:GetAuthorizationToken` through the AWS
  default credential chain: on EC2, the instance role, which needs that
  permission, and `ecr:BatchGetImage` plus `ecr:GetDownloadUrlForLayer` on
  the repository for the runners' pull. A token lasts 12 hours; dude
  reuses it until an hour before it expires. Set `DUDE_ECR_ROLE_ARN`
  (recommended; see below) to mint tokens as a pull-only role instead.
- `static`: `DUDE_REGISTRY` (a host with an optional port, e.g. `ghcr.io`;
  no scheme or path) and `DUDE_REGISTRY_CREDENTIAL` (`user:password`, or
  a bare token, which lux sends as the password with the user `lux`), for
  GHCR and other registries with long-lived tokens.

#### A pull-only role for ECR

An ECR token carries every ECR permission of the identity that minted it,
and it reaches every runner a Run lands on. A host role that builds and
pushes images would hand runners a token that can push too. Mint as a role
that can only pull:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*" },
    {
      "Effect": "Allow",
      "Action": ["ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:BatchCheckLayerAvailability"],
      "Resource": "arn:aws:ecr:<region>:<account>:repository/<agent-image-repository>"
    }
  ]
}
```

Its trust policy must let the host role assume it, and the host role needs
`sts:AssumeRole` on the pull role's ARN. Set
`DUDE_ECR_ROLE_ARN=arn:aws:iam::<account>:role/<name>` with
`DUDE_REGISTRY_AUTH=ecr`: the orchestrator refuses to start if the ARN is
malformed or the mode is not `ecr`. It assumes the role through STS with the
host's credentials, reuses the role's credentials until five minutes before
they expire, and calls `GetAuthorizationToken` with them. At startup it logs
which identity mints tokens (`minted_by`: the role's ARN, or
`host credentials`). A failed `AssumeRole` is treated as a failed mint (below):
starts and resumes wait and are retried, and no token is minted with the
host's credentials instead.

The login goes to lux as the secret `DUDE_REGISTRY_AUTH`, on the Run's
submit and again on every resume (lux keeps no secret), so a Run parked
for days resumes with a new token. This holds for every lux Run dude
starts: agents' phase Runs and branch previews alike. A preview runs its
project's preview image, else its `runtimeImage`, else `DUDE_AGENT_IMAGE`,
and is resumed, when a person starts a server on a parked one, with a
fresh login. The login is the runner's: it never enters the container.
dude neither logs nor stores it. Only images in that registry get it: a
project whose `runtimeImage` or preview image is elsewhere (`node:22` from
Docker Hub) pulls without.
A Run is resumed with the login it was started with: one started without a
login resumes without. One started with a login waits, paused, until the
same login is configured again: if the orchestrator restarts with
`DUDE_REGISTRY_AUTH=none`, or logging in to another registry, its resume is
not sent to lux, the orchestrator logs a warning naming the registry and
`DUDE_REGISTRY_AUTH`, and checks again every minute. Restore the setting
and restart, and the Run resumes with a fresh token. A parked preview in
the same state stays parked, its requested servers kept, with a `parked
preview not resumed` warning, and is checked again every minute.

A failed mint (`AssumeRole` or `GetAuthorizationToken`) holds back every
start and resume that needs the login: Runs stay pending or paused and
previews parked; none is submitted or resumed without it, and none fails
for it. The provider backs off once for all of them: while the back-off
lasts, no AWS call is made, however many Runs wait. A failure that may pass
(ECR or STS unreachable, throttled, a 5xx) is retried after 5 seconds,
doubling to 5 minutes, with jitter, and logged as a warning on its first
failure. A refusal only an operator fixes (`AccessDenied` from STS,
`AccessDeniedException` from ECR, an unknown access key, STS disabled in
the region) is logged once at **error** level (`registry login refused by
AWS`) and retried every 15 minutes, so a fixed IAM policy or trust takes
effect without a restart. A new token must be valid for more than an hour;
one that is not (clock skew) is a failed mint too.

### lux version for private images

`image.registryAuth` and the runner-only secret it names are new in lux's
RunSpec. Both lux's control plane (`luxd`) and **every runner** that can
take a dude Run must understand them. A control plane without them
refuses the submit or drops the login (lux's current schema rejects unknown
fields: `additionalProperties: false`). A runner without them cannot pull the
private image, and a resume can land on any runner.
The OpenAPI document dude was built against (lux `main`, `info.version:
dev`) pins no release, so the minimum version cannot be stated here:
**verify** it in lux's changelog, as the first lux release whose
`Image` schema has `registryAuth` and whose `Secret` has `runnerOnly`, and
check the running version of `luxd` and of each runner host.

Upgrade or drain every runner host before setting `DUDE_REGISTRY_AUTH` to
`ecr` or `static`, or pointing `DUDE_AGENT_IMAGE` at a private registry.

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

### Bun

The release's `dude-backend` and `dude-migrate` carry their own Bun (the
version pinned in `.github/workflows/release.yml`); nothing else is
installed. Run from source, the backend needs **Bun 1.4.0 or later**
(`engines.bun` in `package.json`). Bun 1.3.x's S3 client fails every
upload to a store that answers `Connection: close`, as versitygw does: the
object is stored, but Bun reports `ConnectionClosed` and the upload answers
500. With `s3.bucket` set, the backend refuses to start on an earlier Bun,
naming the version it needs; without it, no S3 request is made and it
starts.

The orchestrator reaches out to `LUX_URL`, to GitHub's API
(`https://api.github.com`, or the organization's stored `apiBaseUrl`), to
ECR's API (`api.ecr.<region>.amazonaws.com`) and the instance metadata
service with `DUDE_REGISTRY_AUTH=ecr`, STS (`sts.<region>.amazonaws.com`)
with `DUDE_ECR_ROLE_ARN`, and
to the push services of browsers that asked for notifications. The backend
reaches out to the orchestrator, and to GitHub's API when a person verifies
a stored credential.

## Branch previews

A branch preview serves a task's branch with the project's servers marked
to start in previews. With a lux that serves previews (its
`preview.domain`), each of those servers is a **lux server of its own**
(lux's `/v1/servers`, marcioapm/lux#41) at a hostname dude chooses, one DNS
label under the preview domain:

```
<server>-<task key>-<project slug>.<preview domain>     web-jerv-2-jervasion.preview-absmartly.dev
```

The domain's wildcard certificate need cover one level only. Each part is
lowercased with anything outside `a-z0-9` made `-`; a label longer than 63
characters is cut and ends in `-` and 8 hex characters of a hash of the
server's name and the task's and project's ids, so it is stable and
distinct. A project's slug is unique in its organization and never changes,
so two projects sharing a key prefix get different names; another
organization's project of the same slug may hold the name, and lux refusing
a hostname another server has (`hostname_taken`) makes dude choose a
second, hashed one. A server keeps the hostname it was created at: renaming
a project's key prefix leaves existing previews' URLs as they are, and
previews made before names used key and slug keep their id-based ones. dude
keeps lux's server id, stores the full hostname and URL lux returns, and
finds a preview's servers by their `dude.preview` label; it never reads a
hostname back.

- **Declaring a preview** creates its servers (`wake: request`, `lifetime:
  owner`, `idleAfter` the project's idle limit, `expireAfter` 30 days,
  labels `dude.org`, `dude.project`, `dude.task`, `dude.preview`) and
  nothing else: no Run, no host.
- **Opening a URL** (signed in to lux's previews) shows lux's waking page,
  and lux tells dude on its event feed (`GET /v1/events`). dude resumes the
  preview's Run, every checkout synced to the task's branch, or, the first
  time, submits one and attaches the servers. A Run lux refuses to resume
  (a 4xx: `no_snapshot`, `not_resumable`, `secrets_required`), or no longer
  has, or that can never run again (`succeeded`, `cancelled`), is replaced:
  a new Run is submitted and the servers attached to it. The page drops into
  the app once it serves.
- **A Run that fails to start**: lux took the resume or the submit, and the
  Run then ended `failed` or `lost` before it ran (a container that would
  not start, an image that would not pull, a host lost mid-start). Resuming
  it would repeat what failed, so the next wake cancels it and submits a new
  Run instead. dude asks for that wake itself, after 1 s, then 2 s, so
  whoever is on the waking page gets the new Run: up to 3 starts in a row
  for one request. After the third, dude stops. The preview shows asleep,
  with the error (`the preview's Run failed to start (<lux's reason>) 3
  times in a row`), and lux's page says "no answer" once its 5-minute wake
  timeout passes. From then on each new request, a person starting a server
  or a URL opened after "no answer", tries one more new Run. The count
  resets once a start runs. A Run that ran and then crashed or lost its
  host is resumed from its snapshot as before: only a start that never ran
  counts. "Never ran" is read from lux's own order of the Run's events
  (`runs.lux_start_event`, `runs.lux_ran_event`): no `running` since the
  `resuming` (or the new Run's first event) that began the start. It does
  not depend on dude's status, or on whether the feed or the wake's own
  answer from lux is recorded first; each event is applied once.
- **Unused**: lux reports a server idle after `idleAfter` without a
  request; once every server of the preview is, dude stops the Run (its
  checkout and state volume kept for the next wake). lux reports idleness
  only for a server that is ready, so one that never becomes ready
  (starting or unreachable) counts as idle once it has had no request for
  `idleAfter`, and an exited one at once.
- **A new commit** on the task's branch (an agent's, or a push the forge
  reports on its pull request) is synced into a running preview at once; a
  sleeping one gets it on its next wake.
- **Ending**: a preview nobody has opened for `previews.reap_after`
  (default 7 days), a stopped one, or one whose task is done, failed or
  aborted has its lux servers deleted — their URLs then say "This preview
  is gone" — and then its Run cancelled. lux's own 30-day expiry is the
  safety net; a server lux expires or someone deletes ends its preview.

Every orchestrator follows the feed; each event is applied once, keyed in
the database, and a wake is acted on by one orchestrator. The feed's
position is kept in `lux_feed`: the highest event id whose lux time is
more than 15 seconds old (lux can commit a lower id after a higher one for
up to its 10-second feed settle), written at most once a second. A restart
replays at most that much, applied once, and misses no event.

**Requirements.** dude needs a lux with `/v1/servers`: the orchestrator
refuses to start against an older one (404 or 405 there), naming the
release. A lux that does not answer at startup is asked again, backing off
to once a minute, rather than stopping the orchestrator. When lux reports
`previews: true` in `GET /v1/whoami`, dude sends only the DNS label and lux
appends its domain. `previews.domain` (`DUDE_PREVIEW_DOMAIN`) is optional,
including when Cloudflare Access makes lux's `previewDomain` null. If set,
it must still match a domain lux reports; if lux reports null, dude ignores
the setting for naming and logs that at INFO. Startup logs the naming mode.
`previews: false` disables wake-on-request previews.

For an older lux without the `previews` field, dude sends full hostnames:
`previews.domain` is needed only if lux's `previewDomain` is null; otherwise
lux's domain is used. A configured domain must match lux's when both are
available. Without either, previews keep the old path below.

**Switching over.** Previews created before this release, and previews of
a project with no server marked to start in previews, keep the old path
until they end: a Run with its servers in its spec, at
`<server>-<run>.<domain>`, parked by dude after the project's idle limit
and woken by a person starting a server. Nothing is migrated: stop and
start an old preview to move it to the new path.

## Postgres

Postgres 17 with the pgvector extension is what the tests run against
(`pgvector/pgvector:pg17-trixie`, the same Debian as `postgres:17`, so an
existing database keeps its collation): memory's index needs it
(migration 054 creates the extension). pgvector is not a trusted extension,
so creating it needs a superuser: where the owner below is not one (a
managed Postgres, RDS, Cloud SQL), a superuser creates it once in dude's
database, `CREATE EXTENSION vector;`, before the first migration that needs
it (054), and on a managed service it must be allowed there first. One
database, two login roles:

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
   release. If its `FEATURES` lists `validate`, run both `validate`s
   ([below](#validating-a-configuration)) and stop on a failure.
2. Run the new `dude-migrate` with `DATABASE_URL` as the owner. It applies
   what is new, each file in a transaction, and is safe to run again. It
   refuses a migration whose file changed after it was applied.
   `dude-migrate --status` lists applied and pending migrations.
3. Switch to the new release and, if the agent image was rebuilt with the
   new `dude` CLI, set `DUDE_AGENT_IMAGE` to its digest; then restart
   `dude-orchestrator` and `dude-backend`.
   A Run that has started keeps its image across resumes.

Between steps 2 and 3 the old processes run against the new schema.

### Validating a configuration

Before migrating or switching, run the new release's `validate` against the
host's real configuration, as the service user with the services'
environment files (only if its `FEATURES` lists `validate`):

```
dude-orchestrator validate
dude-backend validate
```

Each reads the configuration exactly as its startup does (`DUDE_CONFIG`,
else `/etc/dude/dude.toml`, and the environment) and runs every check
startup makes before it opens a connection: unknown keys, types and
values, required settings, and for the orchestrator durations, the machine
rate, embeddings and their dimensions, the registry login's inputs and the
LLM URL; for the backend, the whole `[auth]` section. It connects to
nothing (Postgres, lux, S3, the LLM, AWS, Cloudflare), listens on nothing,
writes nothing, and exits.

- Exit 0: `ok: <file>` (or `ok: no file`) on stdout; each loader warning
  (a secret in a readable file, a retired setting) on stderr as
  `warning: …`, naming keys, never values.
- Exit 1: startup's own error on stderr. Keep the running release.
- Exit 2: an unexpected argument; usage on stderr.

What it cannot check needs a connection: that the database, lux and the
rest are reachable and accept the credentials, and, with Cloudflare Access,
that `auth.default_organization` names an existing organization
(`dude-backend validate` says `not checked: default_organization exists`).
A migration that fails, or a service that fails to start, is still possible
after a clean validate.

### Turning on a registry login

Before the first `DUDE_REGISTRY_AUTH=ecr` (or `static`):

1. Upgrade lux's control plane and every runner host to a version with
   `image.registryAuth` ([lux version for private images](#lux-version-for-private-images)),
   or drain the hosts that cannot be upgraded.
2. Set the login and restart the orchestrator:
   - `ecr`: the IAM policies ([A pull-only role for ECR](#a-pull-only-role-for-ecr)),
     `DUDE_REGISTRY_AUTH=ecr`, `DUDE_ECR_ROLE_ARN` and `DUDE_AGENT_IMAGE`.
     Its startup log says `agent images are pulled with a registry login`
     with the expected `minted_by`, and no `registry login refused by AWS`
     error follows.
   - `static`: `DUDE_REGISTRY_AUTH=static`, `DUDE_REGISTRY` and
     `DUDE_REGISTRY_CREDENTIAL`, and no `DUDE_ECR_ROLE_ARN` (the
     orchestrator refuses to start with one). Its startup log says `agent
     images are pulled with a registry login`.
3. Against the real lux, with a project whose image is the private one:
   - start one Run, see it pull and start; pause it, resume it, and see it
     start again (a resume carries a new login);
   - start one branch preview, see its autostart server come up; let it
     park (or stop its servers), start a server on it, and see it resume.

   This is the contract check dude's own tests cannot make: they run
   against a fake lux, which accepts a spec real lux may refuse and never
   pulls an image. Also check, on a runner host, that the Run's container
   has no `DUDE_REGISTRY_AUTH` in its environment or files.
4. Only then point more projects at the private image.

## Configuration

Both processes read **one TOML file**: `DUDE_CONFIG` if it is set, else
`/etc/dude/dude.toml` if it exists, else none. [`dude.example.toml`](dude.example.toml)
lists every key with its default and its variable.

**Precedence.** Every setting has an environment variable, and a variable
that is set and not empty overrides the file's key; a key in neither is its
default. An empty string in the file (`listen = ""`) counts as unset, as an
empty variable does. Lists (`agent.egress`, `factory.logins`) are TOML arrays in the file
and comma-separated in the variable. Booleans in a variable are `true`,
`false`, `on`, `off`, `1` or `0`. With no file, the environment alone
configures dude, exactly as before the file existed. The backend relies on
Bun's TOML parser, which reads special float values (`inf`, `nan`) as 0, so
write ports as plain integers.

**Strict.** Each process stops at startup, naming the file key and variable,
on: `DUDE_CONFIG` set to a file it cannot read; a key neither process knows;
a value of the wrong type or an invalid value (from the file or the
variable) for a key it uses. Each process checks only the keys it uses, so
the backend accepts the orchestrator's sections and the reverse. A key only
the other process uses is that process's to refuse.

**Secret** marks a value that must be kept out of logs and out of files
others can read. Either process logs a warning at startup when the file is
readable by group or others (mode `0644`, `0640`) and holds a secret key; it
does not refuse to start.

| File key | Variable | Default | Used by | |
| --- | --- | --- | --- | --- |
| `database.url` | `DATABASE_URL` | required | both | Postgres, as `dude_app`. **Secret** (password). |
| `backend.port` | `PORT` | `3000` | backend | Listening port, on all interfaces. |
| `backend.web_dir` | `DUDE_WEB_DIR` | off | backend | Serve the web app from this directory: `<prefix>/share/dude/web`. Unset, the backend serves only the API. |
| `orchestrator.url` | `DUDE_ORCHESTRATOR_URL` | none | backend | The orchestrator's internal API, e.g. `http://127.0.0.1:3100`. Unset, anything that changes what runs answers 503. |
| `orchestrator.token` | `DUDE_ORCHESTRATOR_TOKEN` | required (orchestrator) | both | The token the backend authenticates with, the same in both; also signs agents' tool tokens unless `tools.key` is set. **Secret.** |
| `orchestrator.listen` | `DUDE_ORCHESTRATOR_LISTEN` | `127.0.0.1:3100` | orchestrator | Internal API address. |
| `orchestrator.pr_reconcile` | `DUDE_PR_RECONCILE` | `15m` | orchestrator | How often open pull requests are re-read as a backstop to webhooks (a Go duration). |
| `orchestrator.park_after` | `DUDE_PARK_AFTER` | the delivery policy's | orchestrator | Grace before a Run waiting on a person is parked, for projects that set none (a Go duration). |
| `orchestrator.idle_after` | `DUDE_IDLE_AFTER` | the delivery policy's | orchestrator | Quiet time before an idle Run is nudged and parked, for projects that set none. |
| `orchestrator.diff_every` | `DUDE_DIFF_EVERY` | `15s` | orchestrator | How often a working agent's diff is read besides after its edits. |
| `orchestrator.machine_usd_per_hour` | `DUDE_MACHINE_USD_PER_HOUR` | `0.20` | orchestrator | What an hour of a lux host costs, recorded with each Run; not negative. |
| `orchestrator.lux_cost_every` | `DUDE_LUX_COST_EVERY` | `2m` | orchestrator | How often an agent's Run's cost is read from lux (`GET /v1/runs/{id}/cost`), until lux reports it final or eight days after the Run ended; positive. |
| `s3.bucket` | `DUDE_S3_BUCKET` | off | both | The bucket people's photos, projects' images and the images people send agents (steers, answers, a task's prompt) are kept in. The backend writes and serves them; the orchestrator reads the images it gives lux. Unset, uploads answer 503, faces show initials, and the composer's attach button is off ("Image storage isn't set up"). |
| `s3.endpoint` | `DUDE_S3_ENDPOINT` | AWS | both | For MinIO, versitygw and other S3-compatible stores (path-style). versitygw needs Bun ≥ 1.4.0 (see [Bun](#bun)); the release has it. |
| `s3.region` | `DUDE_S3_REGION` | `us-east-1` | both | |
| `s3.access_key`, `s3.secret_key` | `DUDE_S3_ACCESS_KEY`, `DUDE_S3_SECRET_KEY` | off | both | Explicit credentials for local S3-compatible stores such as MinIO; set both. The secret key is a **secret**. Unset, each process obtains temporary EC2 instance-role credentials through IMDSv2; neither uses the AWS environment credential chain. The orchestrator needs `s3:GetObject` only; the backend `s3:PutObject`, `s3:GetObject` and `s3:DeleteObject`. |
| `lux.url` | `LUX_URL` | required | orchestrator | The lux control plane. |
| `lux.api_key` | `LUX_API_KEY` | required | orchestrator | A lux API key with the `run` scope. **Secret.** |
| `lux.console_url` | `LUX_CONSOLE_URL` | `lux.url` | orchestrator | lux's console, for the "Open terminal in lux" links on a task's servers (`<url>/runs/<luxRunId>/terminal`). |
| `previews.domain` | `DUDE_PREVIEW_DOMAIN` | optional; older lux's `previewDomain` | orchestrator | Needed only for lux predating relative hostnames when it does not report its domain. New lux receives only `<server>-<task key>-<project slug>` and returns the full hostname and URL (see [Branch previews](#branch-previews)). If set, must match a domain lux reports; ignored for naming when new lux reports null. |
| `previews.reap_after` | `DUDE_PREVIEW_REAP_AFTER` | `168h` | orchestrator | A branch preview nobody has opened for this long is ended and its lux servers deleted; positive. lux's own `expireAfter` (30 days, set by dude) is the safety net. |
| `llm.url` | `DUDE_LLM_URL` | none | orchestrator | The LLM API agents use, as a base URL before the API path, e.g. `https://llmproxy.example.com/v1`; must be http(s). Given to each Run as the plain env var `DUDE_LLM_URL`; its host is agents' model egress. See [Agent image contract](#agent-image-contract). |
| `llm.key` | `DUDE_LLM_KEY` | none | orchestrator | That API's key. Given to each Run as a lux secret delivered as the env var `DUDE_LLM_KEY`: never in the spec's env or labels, never stored by lux. **Secret.** |
| `embeddings.url` | `DUDE_EMBEDDINGS_URL` | `llm.url` | orchestrator | An OpenAI-compatible embeddings API, before `/embeddings` (llm-proxy: `https://…/v1`), for a provider other than the agents'. `off`, or neither this nor `llm.url` set, and memory is searched by words alone. |
| `embeddings.key` | `DUDE_EMBEDDINGS_KEY` | `llm.key`, when the embeddings URL is `llm.url` or on its origin | orchestrator | Its key: the deployment's own virtual key, never a person's. `llm.key` is never sent to another origin (scheme, host, port): an explicit `embeddings.url` elsewhere without this key fails startup. With only `llm.url` and no key at all, embeddings are off. **Secret.** |
| `embeddings.model` | `DUDE_EMBEDDINGS_MODEL` | `gemini-embedding-2` | orchestrator | Changing it re-embeds everything in the background; search keeps working by words meanwhile. |
| `embeddings.dimensions` | `DUDE_EMBEDDINGS_DIMENSIONS` | `768` | orchestrator | The index's size: only 768 is accepted, another is a migration. |
| `agent.image` | `DUDE_AGENT_IMAGE` | `localhost/dude-runtime:dev` | orchestrator | Image for agents when a project names none: the operator's own, pinned by digest. |
| `agent.timeout` | `DUDE_AGENT_TIMEOUT` | none | orchestrator | A limit on a Run's running time, passed to lux. |
| `agent.egress` | `DUDE_AGENT_EGRESS` | none | orchestrator | Hosts agents may reach besides `llm.url`'s host; `*` turns egress filtering off. With neither this nor `llm.url`, egress is unrestricted. |
| `agent.nested_containers` | `DUDE_AGENT_NESTED_CONTAINERS` | `false` | orchestrator | Agents may run containers themselves (rootless Docker or Podman in the Run), for test suites that start their own services. Sets lux's `sandbox.nestedContainers` on every agent Run, so lux places them only on hosts whose runner offers nested containers: with none, Runs wait for one. The agent image must carry the engine. Preview servers are unaffected. |
| `registry.auth` | `DUDE_REGISTRY_AUTH` | `none` | orchestrator | How lux logs in to pull agent images: `none`, `ecr` or `static`. See [Private agent images](#private-agent-images). |
| `registry.host` | `DUDE_REGISTRY` | none | orchestrator | `static` only: the registry host, e.g. `ghcr.io`. |
| `registry.credential` | `DUDE_REGISTRY_CREDENTIAL` | none | orchestrator | `static` only: `user:password` for `registry.host`. **Secret.** |
| `registry.ecr_role_arn` | `DUDE_ECR_ROLE_ARN` | none (host credentials) | orchestrator | `ecr` only: a pull-only IAM role to assume and mint tokens as. See [A pull-only role for ECR](#a-pull-only-role-for-ecr). |
| `tools.listen` | `DUDE_TOOLS_LISTEN` | off | orchestrator | Address the agent tools listen on, e.g. `0.0.0.0:3200`. Unset, agents get no dude tools. |
| `tools.url` | `DUDE_TOOLS_URL` | none | orchestrator | The tools as agents' containers reach them, e.g. `http://10.0.1.5:3200`. Unset, agents get no dude tools. Must not be the lux host or lux's own address: lux never lets a Run reach either. |
| `tools.service` | `DUDE_TOOLS_SERVICE` | `true` | orchestrator | `false` (`off`) for a lux without workload services: the agent is then handed the tool token directly. |
| `tools.key` | `DUDE_TOOLS_KEY` | `orchestrator.token` | orchestrator | Key that derives each Run's tool token. Changing it invalidates live Runs' tokens. **Secret.** |
| `vapid.public_key`, `vapid.private_key` | `DUDE_VAPID_PUBLIC_KEY`, `DUDE_VAPID_PRIVATE_KEY` | made once, kept in `push_config` | orchestrator | Web Push keys. The private key is a **secret**. Changing them invalidates existing browser subscriptions. |
| `vapid.subject` | `DUDE_VAPID_SUBJECT` | `mailto:dude@localhost` | orchestrator | Who push services may contact (`mailto:` or `https:`). |
| `factory.logins` | `DUDE_FACTORY_LOGINS` | none | orchestrator | GitHub logins whose PR comments are the factory's own, and wake no agent. |
| `auth.provider` | `DUDE_AUTH_PROVIDER` | `api_key` | backend | `api_key` or `cloudflare_access` ([README](../README.md)). API keys keep working either way. Access settings below without a provider are refused. |
| `auth.public_url` | `DUDE_AUTH_PUBLIC_URL` | required with Access | backend | The https origin people open dude at; changes signed in by Access must come from it. |
| `auth.auto_create` | `DUDE_AUTH_AUTO_CREATE` | `true` | backend | Someone Access lets in who is not yet one of the organization's people becomes a member; `false` refuses them. |
| `auth.default_organization` | `DUDE_AUTH_DEFAULT_ORGANIZATION` | required with Access | backend | The slug of an existing organization Access sign-ins belong to; startup fails if none has it. |
| `auth.cloudflare_access.team` | `DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM` | required with Access | backend | The Zero Trust team name (one DNS label). |
| `auth.cloudflare_access.aud` | `DUDE_AUTH_CLOUDFLARE_ACCESS_AUD` | required with Access | backend | The Access application's audience (AUD) tag. |

Also read from the environment only, and not settings: `AWS_PROFILE` and
the other `AWS_*` variables (the orchestrator's AWS default credential chain
with `registry.auth = "ecr"`; the region is always the image's registry's),
and the retired `DUDE_OPENCODE_AUTH`/`DUDE_OPENCODE_CONFIG`, ignored with a
warning. The `dude` CLI inside Run containers is configured by lux per Run.

**Retired settings.** A setting a release removes is still accepted by the
next release, from the file or the environment: it is ignored, and each
process logs `retired: <key>; remove it` (the key or variable, never its
value). The release after refuses it as an unknown key. None is retired
today.

### Moving a deployment to the file

The file can be committed with the deployment (for example in the aiverse
repository) and installed as `/etc/dude/dude.toml`, readable by the service
user only if it holds anything sensitive. Put in it everything that is not a
secret, and keep in the environment files only the secrets and the values
known only at deploy time. The secrets are:

- `database.url` (`DATABASE_URL`: holds the password)
- `orchestrator.token` (`DUDE_ORCHESTRATOR_TOKEN`)
- `lux.api_key` (`LUX_API_KEY`)
- `llm.key` (`DUDE_LLM_KEY`)
- `embeddings.key` (`DUDE_EMBEDDINGS_KEY`)
- `vapid.private_key` (`DUDE_VAPID_PRIVATE_KEY`)
- `tools.key` (`DUDE_TOOLS_KEY`)
- `s3.secret_key` (`DUDE_S3_SECRET_KEY`)
- `registry.credential` (`DUDE_REGISTRY_CREDENTIAL`)

Moving is safe one key at a time: a variable still set wins over the file,
so an existing environment file keeps working unchanged. Remove a variable
only once its key is in the file. Both processes log `configuration file
read` with its path at startup; check it before removing variables.

### dude-orchestrator

At startup the orchestrator logs which variables the embeddings URL and key
came from (`url_from`, `key_from`), never the key.

With `DUDE_EMBEDDINGS_URL` and `DUDE_EMBEDDINGS_KEY` in its environment,
`go test ./internal/memory -run RealEmbedder` checks the real embedder end
to end: a query sharing no word with a memory finds it by meaning.

### Agent image contract

dude gives every real agent Run these, and nothing else about its model:

- `DUDE_LLM_URL` (plain env) and `DUDE_LLM_KEY` (a lux env secret), from the
  orchestrator's variables of the same names;
- `OPENCODE_CONFIG_CONTENT`, the Run's model and effort as inline OpenCode
  config. The model is the one the role's tier requests, declared under the
  provider its name goes through (`claude-*`: `llm-anthropic`; anything else:
  `llm-openai`), e.g.
  `{"model":"llm-anthropic/claude-opus-5-5","provider":{"llm-anthropic":{"models":{"claude-opus-5-5":{}}}},"agent":{"build":{"reasoningEffort":"high"}}}`
  (effort `max` is sent as `high`; no effort, no `agent` key). OpenCode
  deep-merges it over its file config, so a model the file declares keeps its
  `limit` and `reasoning`.

Provider definitions are not secret and belong to the image. An agent image
sets `OPENCODE_CONFIG` to a config file baked into it whose providers read
the URL and key from the environment. The dev image
(`images/runtime/opencode.json`) and the production image define the same
two providers, `llm-anthropic` (`@ai-sdk/anthropic`) and `llm-openai`
(`@ai-sdk/openai-compatible`); dude writes every Run's model under one of
them:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "autoupdate": false,
  "provider": {
    "llm-anthropic": {
      "npm": "@ai-sdk/anthropic",
      "options": { "baseURL": "{env:DUDE_LLM_URL}", "apiKey": "{env:DUDE_LLM_KEY}" },
      "models": { "claude-sonnet-5": { "name": "Claude Sonnet 5", "attachment": true,
        "modalities": { "input": ["text", "image"], "output": ["text"] } } }
    },
    "llm-openai": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "{env:DUDE_LLM_URL}", "apiKey": "{env:DUDE_LLM_KEY}" },
      "models": { "gpt-5.6-sol": { "name": "GPT 5.6 Sol", "attachment": true,
        "modalities": { "input": ["text", "image"], "output": ["text"] } } }
    }
  }
}
```

Every model must declare `"attachment": true` and
`"modalities": {"input": ["text", "image"], "output": ["text"]}`. OpenCode
does not know a custom provider's models, and without both keys it treats
one as text only: the images people send with a steer, an answer or a
task's prompt reach the agent, and the model answers "This model does not
support image input". A custom or production image must declare them for
each model it defines; the dev catalog's are checked by
`apps/control-plane/test/runtime-image.test.ts`.

The file must not live in `/etc/opencode/`: on Linux that is OpenCode's
managed config directory, merged above `OPENCODE_CONFIG_CONTENT`, so anything
it sets would override each Run's model and effort. Both images use
`/usr/local/share/dude/opencode.json`.

A role names a model tier, and a tier the model dude requests, as the proxy
names it (no provider prefix, whitespace or slash; at most 200 characters):
see [`design/model-tiers.md`](design/model-tiers.md). The model need not be in
the image's file: dude declares it in each Run's inline config, so any name
the proxy serves works without an image change. One the file does declare
keeps the `limit` and `reasoning` it gives it; one it does not gets
OpenCode's defaults — no context limit (so no automatic compaction) and
32000 output tokens a request (OpenCode 1.18.34). Declare a model in the
file when it needs a known context window.

The only names that are not the proxy's are the test harness models
`fake/scripted`, `fake/hang`, `fake/tools`, `fake/request`, `fake/wait`,
`fake/live`, and `fake/ask`, implemented by `orchestrator/internal/fakeagent`.
These are deterministic test/demo agents, not production image providers;
arbitrary `fake/<model>` values are not accepted.

### Upgrading from DUDE_OPENCODE_*

Earlier versions shipped OpenCode's `auth.json` and `opencode.json` into each
Run as the secrets `opencode_auth` and `opencode_config`
(`DUDE_OPENCODE_AUTH`, `DUDE_OPENCODE_CONFIG`, now ignored with a warning).

- Runs parked or paused before the upgrade cannot resume after it: lux holds
  refs to `opencode_auth` and `opencode_config`, answers 422
  `secrets_required` when they are not supplied, and the Run fails. Finish or
  cancel parked real-model Runs before upgrading, or accept that they fail.
- Role models are tiers since migration 069, which made each organization's
  tiers from the models its roles named (see
  [`design/model-tiers.md`](design/model-tiers.md), "Upgrade").
- A Run keeps the URL, model and effort it started with; only the key is
  supplied again on each resume.

### dude-backend

On EC2, set `DUDE_S3_BUCKET` and `DUDE_S3_REGION`, grant the instance role
`s3:PutObject`, `s3:GetObject` and `s3:DeleteObject` on the bucket's objects
only (`arn:aws:s3:::<bucket>/*`), and leave both `DUDE_S3_*KEY` variables
unset.

The instance role is the instance's, not the backend's: every process on the
host that can reach the metadata service — the orchestrator, anything else
running there — gets the same bucket permissions. Keep the bucket's public
access blocked, and never give agent or runner containers access to the
host's metadata service. If the backend needs permissions no
other process on the host may have, run it on its own instance with its own
role.

Set the instance's metadata options to `HttpTokens=required`, so no process
on the host can use IMDSv1; the backend uses IMDSv2 only. Production runs the
backend directly on the host, where the default hop limit of 1 is enough. A
backend in a container needs a metadata hop limit of at least 2 and a route
to `169.254.169.254`, or explicit keys.

Credentials are refreshed from five minutes before expiry, at most once every
30 seconds. A failed refresh is retried after 5 seconds, doubling to at most
60 seconds; meanwhile the current credential is used until 60 seconds before
it expires. When the metadata service is unreachable or denies access beyond
that, uploads and image reads fail (500), deletes of replaced images are
best-effort and leave the old object behind, and `/health` stays green: it
checks only the database. Storage errors are logged as the operation, HTTP
status and a metadata error code or a known S3 error code only; never an
object key, which holds the random token that authorizes serving the image.
`AWS_EC2_METADATA_SERVICE_ENDPOINT` overrides the metadata address for local
tests only; do not point it at an untrusted server.

Photos and project images are small (the browser uploads a 160 px square, at
most 512 KB is accepted), written once under a new key per upload, and served
back through the backend under a token, so the bucket needs no public access
and no CORS. The backend needs `s3:PutObject`, `s3:GetObject` and
`s3:DeleteObject` on it; a replaced image's object is deleted.

Images people send agents are the other files it stores, under
`attachments/<org>/<task>/`: each is the original (at most 10 MB) and the
variant the agent is sent (the browser scales it to at most 2000 px and
4.5 MiB). Only the organisation's members can read them, through the
backend. The backend's one background loop deletes uploads never sent after
24 hours and the objects of every attachment row that is gone — removed, swept,
or with its task (`ON DELETE CASCADE`); a delete storage refuses is retried on
the next pass, every 10 minutes. The orchestrator reads the delivered
variant to give it to lux, so it needs `s3:GetObject` on the bucket too.

With `DUDE_WEB_DIR` set, GET and HEAD requests that match no API route and
are outside `/v1` and `/health` are served from the directory, and unknown
paths get `index.html`. Files under `/assets/` are cached for a year;
everything else is revalidated.

### dude-migrate

| Variable | Default | |
| --- | --- | --- |
| `DATABASE_URL` | required | Postgres, as the owner. **Secret.** |

### Stored in the database, not configured

Organizations' GitHub credentials and webhook secrets (entered through the
API, stored in plaintext today), people's API keys (hashed), and the VAPID
keys when they are not configured.

## Health

`GET /health` on the backend, and on the orchestrator's internal API,
answers 200 `{"status":"ok"}` when it can reach the database, 503
otherwise. The orchestrator logs to stderr (`log/slog` text); a loop that
fails logs `loop failed` and keeps running.
