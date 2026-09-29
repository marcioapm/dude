# Operations

What a deployment of dude needs: the release, the processes, their
configuration, and the order of an upgrade. Building a release is in the
[README](../README.md#releases).

## The release

A GitHub Release per `v*` tag holds:

| Asset | What it is |
| --- | --- |
| `dude_<version>_linux_arm64.tar.gz`, `…_amd64.tar.gz` | The binaries and their data, below |
| `SHA256SUMS` | `sha256sum` output over the tarballs |

Unpack the tarball into a prefix ([layout](../README.md#releases)).
`dude-migrate` carries its migrations inside itself; it reads no SQL from
disk.

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

The orchestrator reaches out to `LUX_URL`, to GitHub's API
(`https://api.github.com`, or the organization's stored `apiBaseUrl`), to
ECR's API (`api.ecr.<region>.amazonaws.com`) and the instance metadata
service with `DUDE_REGISTRY_AUTH=ecr`, STS (`sts.<region>.amazonaws.com`)
with `DUDE_ECR_ROLE_ARN`, and
to the push services of browsers that asked for notifications. The backend
reaches out to the orchestrator, and to GitHub's API when a person verifies
a stored credential.

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
   release.
2. Run the new `dude-migrate` with `DATABASE_URL` as the owner. It applies
   what is new, each file in a transaction, and is safe to run again. It
   refuses a migration whose file changed after it was applied.
   `dude-migrate --status` lists applied and pending migrations.
3. Switch to the new release and, if the agent image was rebuilt with the
   new `dude` CLI, set `DUDE_AGENT_IMAGE` to its digest; then restart
   `dude-orchestrator` and `dude-backend`.
   A Run that has started keeps its image across resumes.

Between steps 2 and 3 the old processes run against the new schema.

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
| `LUX_CONSOLE_URL` | `LUX_URL` | lux's console, for the "Open terminal in lux" links on a task's servers (`<url>/runs/<luxRunId>/terminal`). |
| `DUDE_AGENT_IMAGE` | `localhost/dude-runtime:dev` | Image for agents when a project names none: the operator's own, pinned by digest. |
| `DUDE_REGISTRY_AUTH` | `none` | How lux logs in to pull agent images: `none`, `ecr` or `static`. See [Private agent images](#private-agent-images). |
| `DUDE_REGISTRY` | none | `static` only: the registry host, e.g. `ghcr.io`. |
| `DUDE_REGISTRY_CREDENTIAL` | none | `static` only: `user:password` for `DUDE_REGISTRY`. **Secret.** |
| `DUDE_ECR_ROLE_ARN` | none (host credentials) | `ecr` only: a pull-only IAM role to assume and mint tokens as. See [A pull-only role for ECR](#a-pull-only-role-for-ecr). |
| `AWS_PROFILE`, other `AWS_*` | the instance role | `ecr` only: the AWS default credential chain. The region is always the image's registry's. |
| `DUDE_LLM_URL` | none | The LLM API agents use, as a base URL before the API path, e.g. `https://llmproxy.example.com/v1`; must be http(s). Given to each Run as the plain env var `DUDE_LLM_URL`; its host is agents' model egress. See [Agent image contract](#agent-image-contract). |
| `DUDE_LLM_KEY` | none | That API's key. Given to each Run as a lux secret delivered as the env var `DUDE_LLM_KEY`: never in the spec's env or labels, never stored by lux. **Secret.** |
| `DUDE_AGENT_EGRESS` | none | Comma-separated hosts agents may reach besides `DUDE_LLM_URL`'s host; `*` turns egress filtering off. With neither this nor `DUDE_LLM_URL`, egress is unrestricted. |
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
| `DUDE_EMBEDDINGS_URL` | `DUDE_LLM_URL` | An OpenAI-compatible embeddings API, before `/embeddings` (llm-proxy: `https://…/v1`), for a provider other than the agents'. `off` disables embeddings; `off`, or neither this nor `DUDE_LLM_URL` set, and memory is searched by words alone. |
| `DUDE_EMBEDDINGS_KEY` | `DUDE_LLM_KEY`, when the embeddings URL is `DUDE_LLM_URL` or on its origin | Its key: the deployment's own virtual key, never a person's. `DUDE_LLM_KEY` is never sent to another origin (scheme, host, port): an explicit `DUDE_EMBEDDINGS_URL` elsewhere without this key fails startup. With only `DUDE_LLM_URL` and no key at all, embeddings are off. **Secret.** |
| `DUDE_EMBEDDINGS_MODEL` | `gemini-embedding-2` | Changing it re-embeds everything in the background; search keeps working by words meanwhile. |
| `DUDE_EMBEDDINGS_DIMENSIONS` | `768` | The index's size: only 768 is accepted, another is a migration. |

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
  config, e.g. `{"model":"llm-anthropic/claude-sonnet-5","agent":{"build":{"reasoningEffort":"high"}}}`
  (effort `max` is sent as `high`; no effort, no `agent` key). OpenCode merges
  it over its file config.

Provider definitions are not secret and belong to the image. An agent image
sets `OPENCODE_CONFIG` to a config file baked into it whose providers read
the URL and key from the environment. The dev image
(`images/runtime/opencode.json`) and the production image define the same
two providers, `llm-anthropic` (`@ai-sdk/anthropic`) and `llm-openai`
(`@ai-sdk/openai-compatible`), so model names in settings work in both:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "autoupdate": false,
  "provider": {
    "llm-anthropic": {
      "npm": "@ai-sdk/anthropic",
      "options": { "baseURL": "{env:DUDE_LLM_URL}", "apiKey": "{env:DUDE_LLM_KEY}" },
      "models": { "claude-sonnet-5": { "name": "Claude Sonnet 5" } }
    },
    "llm-openai": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "{env:DUDE_LLM_URL}", "apiKey": "{env:DUDE_LLM_KEY}" },
      "models": { "gpt-5.6-sol": { "name": "GPT 5.6 Sol" } }
    }
  }
}
```

The file must not live in `/etc/opencode/`: on Linux that is OpenCode's
managed config directory, merged above `OPENCODE_CONFIG_CONTENT`, so anything
it sets would override each Run's model and effort. Both images use
`/usr/local/share/dude/opencode.json`.

A role's model in dude's settings is `<provider>/<model>` for a provider that
file defines (`llm-anthropic/claude-sonnet-5` above). dude sends no provider
definitions and no OpenCode files.

### Upgrading from DUDE_OPENCODE_*

Earlier versions shipped OpenCode's `auth.json` and `opencode.json` into each
Run as the secrets `opencode_auth` and `opencode_config`
(`DUDE_OPENCODE_AUTH`, `DUDE_OPENCODE_CONFIG`, now ignored with a warning).

- Runs parked or paused before the upgrade cannot resume after it: lux holds
  refs to `opencode_auth` and `opencode_config`, answers 422
  `secrets_required` when they are not supplied, and the Run fails. Finish or
  cancel parked real-model Runs before upgrading, or accept that they fail.
- Role models in project and organization settings must name a provider the
  image defines (`llm-anthropic/…`, `llm-openai/…`); a provider from a
  person's own OpenCode config no longer exists in the Run.
- A Run keeps the URL, model and effort it started with; only the key is
  supplied again on each resume.

### dude-backend

| Variable | Default | |
| --- | --- | --- |
| `DATABASE_URL` | required | Postgres, as `dude_app`. **Secret** (password). |
| `DUDE_ORCHESTRATOR_URL` | none | The orchestrator's internal API, e.g. `http://127.0.0.1:3100`. Unset, anything that changes what runs answers 503. |
| `DUDE_ORCHESTRATOR_TOKEN` | none | The same token as the orchestrator's. **Secret.** |
| `PORT` | `3000` | Listening port, on all interfaces. |
| `DUDE_WEB_DIR` | off | Serve the web app from this directory: `<prefix>/share/dude/web`. Unset, the backend serves only the API. |
| `DUDE_S3_BUCKET` | off | The bucket people's photos and projects' images are kept in. Unset, uploads answer 503 and faces show initials. |
| `DUDE_S3_ENDPOINT` | AWS | For MinIO, versitygw and other S3-compatible stores (path-style). |
| `DUDE_S3_REGION` | `us-east-1` | |
| `DUDE_S3_ACCESS_KEY`, `DUDE_S3_SECRET_KEY` | off | Explicit credentials for local S3-compatible stores such as MinIO; set both. The secret key is a **secret**. Unset, the backend obtains temporary EC2 instance-role credentials through IMDSv2; it does not use Bun's AWS environment credential fallback. |

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

Photos and project images are the only files the backend stores. They are
small (the browser uploads a 160 px square, at most 512 KB is accepted),
written once under a new key per upload, and served back through the
backend under a token, so the bucket needs no public access and no CORS.
The backend needs `s3:PutObject`, `s3:GetObject` and `s3:DeleteObject` on
it; a replaced image's object is deleted.

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
