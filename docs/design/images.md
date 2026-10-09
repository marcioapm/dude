# Image library

An organisation's images: Containerfiles with versions, built on the dude
host and pushed to the registry. Agent roles, previews and projects name
them by id. This document covers how images are kept, built, finished
with the dude layer and resolved for a Run.

## The model

Migration 068 holds the tables. The shared types and the Containerfile lint
are in `packages/domain/src/images.ts`.

- **`images`**: a name and a description. The name is an immutable slug
  (`^[a-z0-9][a-z0-9-]{0,62}$`), unique in its organisation, because other
  Containerfiles name it as `FROM image:<name>`. `published_version_id` is
  what every user of the image runs; it is NULL until a build publishes.
  Archiving hides an image from pickers, and anything still naming it keeps
  working. There is no way to delete an image.
- **`image_versions`**: a Containerfile (at most 64 KiB), build args (a
  string→string map with at most 50 entries, which are not secrets because
  history shows them) and a note.
  - An image has at most one draft. Saving edits it.
  - "Build & publish" gives the draft the image's next number and queues it.
  - States move `draft → queued → building → pushing → published`, and end
    in `failed`, `superseded` or `cancelled`.
  - `user_ref` is the image without the dude layer, by digest. Children
    build `FROM` it.
  - `can_run_containers` (migration 100): Runs in it may start containers
    (see [Can run containers](#can-run-containers)). Each version keeps its
    own, like its Containerfile.
- **`image_version_parents`**: the images a version is built `FROM`,
  resolved when it is saved, and the parent version its build used.
  Saving refuses an unknown `image:<name>` and a cycle.
- **`image_finals`**: (version, dude layer) → the final image a Run pulls.
  A new dude layer finishes a published version again the first time a Run
  needs it.
- **`image_builds`**: the builder's queue, one line for the whole host.
  - Jobs are `build` or `finish`.
  - Finish jobs go ahead of builds. Within each kind, the oldest goes first.
  - The builder runs one job at a time.
  - Partial unique indexes let two Runs that need the same finish share one
    job.
  - The log (`image_build_log`, one chunk per flush) keeps the last 1 MiB.
  - `image_queue_ahead()` tells an organisation how many jobs are ahead of
    its own, and nothing about whose they are.
- **Where an image is named**, always by id, with the same organisation
  enforced by composite foreign keys:
  - `organizations.default_image_id` (the default base);
  - `projects.runtime_image_id` and `projects.preview_image_id`;
  - `image` in a role's settings, on the same two layers as its model and
    `machineSize` (`default_agent_models`, `agent_models`).
- **Server recipes** have no image of their own. A server runs inside its
  session's Run, so it gets that Run's image.
- **Typed images**: the existing free-text `projects.runtime_image` and
  `preview_settings.image` stay readable. The API keeps or clears them but
  refuses a new value, with a 400 that names `imageId`.

RLS isolates every table per organisation, like every other organisation
table. Admins change images; members read them.

## Publishing

`image_publish(version)` is the one definition of publishing. Both the
backend (publishing an older version again) and the builder (a build that
passed) call it. In one transaction it:

1. marks the version `published` and the previous published version
   `superseded`, and moves `images.published_version_id`;
2. queues a rebuild of every image whose **published** version is built
   `FROM` this image. Each rebuild is a new version with the same
   Containerfile, `source = base_rebuild`, and the note "Rebuild on <name>
   vN". An image that already has a queued version gets no second one: that
   version resolves its parents when it starts.

Publishing an older version again is instant. Its image is still in the
registry, whose tags are immutable and never expire. It is not rebuilt onto
the current base. If it has no final for the current dude layer, the next
Run's finish job makes one.

## The builder

`dude-image-builder` (`orchestrator/cmd/dude-image-builder`) is its own
process. It runs as its own host user and Postgres role `dude_builder`.
The orchestrator and the backend never exec podman. The release ships the
binary and declares `image-builder` in `FEATURES`. Configuration is in
`[builder]` and `[images]` (see `docs/dude.example.toml`).

`dude_builder` has no BYPASSRLS. Each image table has a policy for it
alone, and it may insert `image.*` events. Nothing else is granted.

It loops as follows:

1. Claim the next job with `FOR UPDATE SKIP LOCKED`. While it runs,
   heartbeat it, and every 2 s append the output written since to its log
   (only the heartbeat when there is none).
2. A job the builder is stopped under (SIGTERM, as on a deploy) goes back to
   the queue as it was, never failed. When the builder starts, a job left
   `running` by a builder that died (SIGKILL, a crash) is re-queued once.
   The second time, the job fails with "the builder restarted while
   building it, twice".
3. Check free space. Below `min_free_bytes`, prune every image no container
   uses (`podman image prune -a`, no age filter) and check again. If space
   is still short, fail.

Every image a job builds is removed (`podman rmi`) as soon as nothing local
needs it: the final once pushed, the user image once its final is built.
Children and finishes name the pushed digest, so between jobs the
builder's storage holds only base images and the dude layer, which a pull
brings back.

**Starting.** The builder refuses to start unless `podman info` reports the
`cpu` and `memory` cgroup controllers: without them podman only warns and
ignores the limits below.

A **build** job:

1. Resolve each `FROM image:<name>` to the parent's published `user_ref`,
   never its final, so dude layers do not stack.
2. Write a context holding only the Containerfile. A `COPY` without
   `--from` has nothing to copy. The lint refuses it first.
3. Build under the limits below, then push `<repository>:<version>-user`.
4. Finish it with the current layer, then call `image_publish`.

A **finish** job builds this Containerfile:

```
FROM <user_ref>
COPY --from=<layer> /rootfs/ /
RUN /bin/sh /usr/local/share/dude/setup.sh && <agent's subordinate ids>
ENV OPENCODE_CONFIG=… DISABLE_AUTOUPDATER=1 OPENCODE_DISABLE_AUTOUPDATE=1 HOME=/home/agent
USER agent
WORKDIR /home/agent
```

The same step gives `agent` lines in `/etc/subuid` and `/etc/subgid` when
it has none (`images.SubIDs`): `agent:1:999` and `agent:1001:64535` for uid
1000, every id but 0 and its own below 65536. lux runs each Run in a user
namespace of 65536 ids, so a range above it (useradd's `100000:65536`)
could not be mapped inside a Run. Where another name holds uid 1000 first
(`node`), the lines are by uid. An image's own lines are kept.

It pushes `<repository>:<version>-<layer digest, 12 hex>` and records the
result in `image_finals`. The dude layer (`DUDE_LAYER_IMAGE`) is a `FROM
scratch` image whose `/rootfs/` holds:

- the dude CLI, OpenCode and ripgrep;
- OpenCode's config;
- `setup.sh`, which adds the `agent` user and the git identity.

`HOME` is set because `agent` may be a second name for uid 1000 (`node` on
the node images), and podman takes `HOME` from the first passwd entry with
that uid.

aiverse builds it.

**Limits.** Every build runs with:

- `--memory` and `--memory-swap` set to `memory` (1536m);
- CPU set by `--cpu-period 100000 --cpu-quota <cpus × 100000>`. podman
  build has no `--cpus`.
- processes capped by `--ulimit nproc=4096`. podman build has no
  `--pids-limit`.
- a wall-clock `timeout` (60m).

**Failures.** A failure becomes one sentence on the version and the job:
out of memory at a step, the timeout, a missing base, no `/bin/sh`, no
git, a failed push, or a full disk. A failed build never touches the
published version.

## Can run containers

A version marked "Can run containers" lets Runs in it start containers
with rootless Podman or Docker (lux's `sandbox.nestedContainers`; lux
`docs/runspec.md`, "Nested containers").

- **Where it comes from.** It is saved with the draft. A new draft starts
  from the published version's value; a new image whose Containerfile is
  `FROM image:<x>` starts from x's published value; a base rebuild keeps the
  child's. The editor warns before a build when the box is on, no
  instruction names `podman` or `docker`, and no `FROM` is a library image
  that can (`lacksContainerEngine`).
- **The check.** A build or finish of such a version checks its final image,
  after the dude layer and before the push (stage `checking`): the image
  Runs get, with `agent` and its subordinate ids on it. The builder runs
  itself, a static binary (`dude-image-builder containers-check`), in the
  image, offline, as root, and looks for an engine (`podman`, or
  `dockerd-rootless`), `fuse-overlayfs`, `newuidmap` and `newgidmap` able to
  gain `CAP_SETUID`/`CAP_SETGID` (a file capability, or setuid root), and
  `agent`'s `/etc/subuid` and `/etc/subgid` lines within 65536 ids. Missing
  any, the job fails with one sentence ("Can't run containers: the image
  has no podman or rootless Docker, …"), nothing of the final is pushed, and
  the published version stays. `image_builds.containers_check` keeps
  `{passed, detail}`, `check_seconds` its time.
- **Runs.** Every Run, agent or preview, asks lux for nested containers
  when the image it resolved can: a library image as its published version
  says, recorded in `runs.image` as `canRunContainers`; an image typed by
  hand never; `DUDE_AGENT_IMAGE` when `agent.nested_containers` says so. lux
  keeps it in the Run's stored spec, so a resume asks for what it started
  with. lux places such a Run only on a host that offers nested containers;
  while it has none, the Run's servers view carries lux's reason
  (`run.waitingReason`) and the Run page and a preview's Servers tab say it.
- **What a Run records.** At submit, every Run records whether it may start
  containers (`runs.can_run_containers`), from the stored spec lux returns:
  for a retried submit lux had already taken, the first submit's sandbox. A
  resume keeps it; a preview's new generation records its own. The Run
  page's header then says "Can run containers"; a Run not yet submitted, or
  from before this was recorded, has `canRunContainers: null` and shows
  nothing.
- **Previews keep their containers.** A preview whose image can run
  containers gets a state volume at `/home/agent/.local/share`, with
  `XDG_DATA_HOME` set to it, over each engine's store, so its images,
  containers and data survive sleep. Agent Runs do not.

## Resolving a Run's image

The orchestrator handles this in `orchestrator/internal/images/resolve.go`.
The first set id wins, in this order:

1. the role's image (project, then organisation; the fixer follows the
   implementer);
2. for a preview, the preview image;
3. the project's runtime image;
4. the organisation's default base.

Every library id comes before any typed image. With no id set, the typed
preview or runtime image is used as before, and after it `DUDE_AGENT_IMAGE`.
An organisation that never uses the library runs as it did.

For a library image:

- **Library off.** `DUDE_LAYER_IMAGE` is unset, so the library is off. The
  Run fails before lux with "image library not configured".
- **No published version.** If its first version is queued or building,
  the Run waits on that build; if an admin queues a newer version (which
  cancels the waiting one), it waits on the newer one. With none, or when
  the build fails, the Run fails before lux.
- **Final exists.** If `image_finals` has (published version, current
  layer), the Run uses that digest.
- **No final yet.** Otherwise dude enqueues the finish job, or joins one
  already queued, and keeps the Run back. The Run shows "Preparing image"
  (`run.image_preparing`, `runs.image_build_id`).
  - When the job succeeds, the Run is submitted.
  - When it fails, the Run fails before lux with the build's error, at no
    model cost.

Drafts never hold up a Run.

A branch preview resolves and waits the same way, whether it is submitted
eagerly or woken by a request to its URL (`Previews.imageOutcome`). A
woken preview that waits releases its wake claim and keeps the wake
wanted; once its image is ready the next sweep submits it.

**Builder liveness.** The builder writes a heartbeat (`image_builder`, one
row) every 30 s, idle or busy. When it is older than 2 minutes, the Images
page and a waiting Run say "image builder offline since <time>". A Run or
a preview that has waited (`runs.image_waiting_since`) for 30 minutes while
the builder was offline fails before lux with that sentence.

**The build log.** Each builder flush that has output inserts one row into
`image_build_log` (`start_offset`, `chunk`) and adds its bytes to
`image_builds.log_total`; in the same statement it deletes the chunks that
end before the last 1 MiB. A flush therefore costs its own size in WAL,
not the log's. The build page polls `GET /v1/images/builds/:id?after=<n>`
and gets only the chunks ending after byte n, the first cut at n, while
the kept log still reaches back to n; otherwise the whole kept log.

`runs.image` records `{imageId, name, versionId, version, ref, layer, canRunContainers}` once
the image is resolved, and nothing rewrites it. Resumes therefore use the
same digest, and the Run's page shows the image and version next to its
machine size.

Pulls use the existing registry login (`DUDE_REGISTRY_AUTH=ecr`). The
custom repository is in the same registry as `DUDE_AGENT_IMAGE`.

## Not built

The following are not built:

- builds on lux;
- build secrets;
- `COPY` from a build context;
- deleting images;
- per-organisation repositories;
- multi-arch images;
- moving `dude/agents` into the library.
