"""The image library, through the public API and the built UI, against the
deployed backend, orchestrator and fake lux.

dude-image-builder needs rootless podman and is not part of this suite: its
part — a build passing, a dude layer added — is played here by its own
statements against the database (the version's user image recorded, then
image_publish(); an image_finals row), exactly what it writes. Its real
podman tests are in orchestrator/internal/images (scripts/test-image-builder.sh).

DUDE_LAYER_IMAGE is set for both processes (env.TEST_LAYER), so the library
is on; an organization that names no library image runs as it always did.

Screenshots: with DUDE_SHOTS=<dir> set, the browser tests save the pages
they reach there, at 1440 and 390 wide, light and dark (and compact once).
"""

from __future__ import annotations

import os
import re
from pathlib import Path

import pytest
import requests
from playwright.sync_api import Page, expect

from env import TEST_LAYER
from helpers import ApiClient, execute, query, sign_in, wait_until

# dude-image-builder's heartbeat, as it writes it every 30 s; the suite
# says it is alive unless a test is about it being offline.
BUILDER_ALIVE = "INSERT INTO image_builder (seen_at) VALUES (now()) ON CONFLICT (id) DO UPDATE SET seen_at = now()"

BASE = "FROM debian:bookworm-slim\nRUN apt-get update && apt-get install -y git\n"
SCRIPTED = {r: "fake/scripted" for r in ("implementer", "reviewer", "simplifier")}


def _digest(seed: str) -> str:
    return "sha256:" + (seed.encode().hex() * 64)[:64]


def _log(dsn: str, build_id: str, text: str) -> None:
    """A job's log as dude-image-builder flushes it: one chunk at log_total."""
    execute(dsn, """WITH b AS (UPDATE image_builds SET log_total = log_total + octet_length(%s) WHERE id = %s RETURNING organization_id, log_total)
        INSERT INTO image_build_log (organization_id, build_id, start_offset, chunk)
        SELECT organization_id, %s, log_total - octet_length(%s), %s FROM b""", (text, build_id, build_id, text, text))


def _built(dsn: str, version_id: str, final: bool = True) -> list[dict]:
    """What dude-image-builder does when a version's build passes."""
    user = f"registry.test/dude/custom@{_digest('u' + version_id)}"
    execute(dsn, """UPDATE image_version_parents vp SET parent_version_id = i.published_version_id
        FROM images i WHERE i.id = vp.parent_image_id AND vp.version_id = %s""", (version_id,))
    execute(dsn, "UPDATE image_versions SET user_ref = %s, built_at = now() WHERE id = %s", (user, version_id))
    for build in query(dsn, """UPDATE image_builds SET state = 'succeeded', finished_at = now(), started_at = now() - interval '2 minutes',
        build_seconds = 104, push_seconds = 12, layer_ref = %s
        WHERE image_version_id = %s AND kind = 'build' AND state IN ('queued', 'running') RETURNING id""", (TEST_LAYER, version_id)):
        _log(dsn, build["id"], "build · podman, rootless · 1.5 CPUs · 1.5 GB memory · linux/arm64\nSTEP 1/2: FROM debian:bookworm-slim\nSTEP 2/2: RUN apt-get install -y git\npushed " + user)
    if final:
        execute(dsn, """INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref)
            SELECT organization_id, id, %s, %s FROM image_versions WHERE id = %s""",
                (TEST_LAYER, f"registry.test/dude/custom@{_digest('f' + version_id)}", version_id))
    return query(dsn, "SELECT * FROM image_publish(%s)", (version_id,))


def _image(client: ApiClient, name: str, containerfile: str = BASE, **body) -> dict:
    made = client.post("/v1/images", {"name": name, "containerfile": containerfile, "note": "First version", **body})
    assert made.status_code == 201, made.text
    return made.json()


def _publish_first(client: ApiClient, dsn: str, name: str, containerfile: str = BASE, final: bool = True, **body) -> dict:
    image = _image(client, name, containerfile, **body)
    queued = client.post(f"/v1/images/{image['image']['id']}/build")
    assert queued.status_code == 201, queued.text
    _built(dsn, queued.json()["versionId"], final)
    return client.get(f"/v1/images/{image['image']['id']}").json()


def _submitted_spec(env, dsn: str, task_id: str) -> dict | None:
    rows = query(dsn, "SELECT lux_run_id FROM runs WHERE task_id = %s AND phase = 'implement' AND lux_run_id IS NOT NULL", (task_id,))
    if not rows:
        return None
    return requests.get(f"{env.fake_lux_url}/v1/runs/{rows[0]['lux_run_id']}",
                        headers={"authorization": f"Bearer {env.lux_key}"}, timeout=10).json()["spec"]


# ---------------------------------------------------------------------------
# Through the API, with the fake lux
# ---------------------------------------------------------------------------


def test_a_run_on_the_default_base_waits_for_its_dude_layer_then_runs_its_final(client: ApiClient, env, owner_dsn: str):
    base = _publish_first(client, owner_dsn, "acme-base", final=False)
    assert client.post(f"/v1/images/default/{base['image']['id']}").status_code == 200
    project = client.create_project(name="Greeter", slug=f"greeter-{os.urandom(3).hex()}", agentModels=client.on_models(SCRIPTED))
    task = client.create_task(project["id"], "Say hello")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    # No final for the current layer: the Run waits, pending, on a finish job.
    def waiting():
        rows = query(owner_dsn, "SELECT id, image_build_id, lux_run_id FROM runs WHERE task_id = %s AND phase = 'implement'", (task["id"],))
        return rows[0] if rows and rows[0]["image_build_id"] else None

    run = wait_until(waiting, timeout=60, message="the implementer never waited on its image")
    assert run["lux_run_id"] is None
    detail = client.get_run(run["id"])
    assert detail["status"] == "pending"
    assert detail["preparingImage"]["imageName"] == "acme-base" and detail["preparingImage"]["version"] == 1
    jobs = query(owner_dsn, "SELECT kind, state, layer_ref FROM image_builds WHERE id = %s", (run["image_build_id"],))
    assert jobs == [{"kind": "finish", "state": "queued", "layer_ref": TEST_LAYER}]
    assert any(e["eventType"] == "run.image_preparing" for e in client.get(f"/v1/events?runId={run['id']}").json()["events"])

    # The builder adds the layer: the Run goes to lux on the final, by digest, and records it.
    final = f"registry.test/dude/custom@{_digest('late')}"
    version = base["image"]["published"]["versionId"]
    execute(owner_dsn, """INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref)
        SELECT organization_id, id, %s, %s FROM image_versions WHERE id = %s""", (TEST_LAYER, final, version))
    execute(owner_dsn, "UPDATE image_builds SET state = 'succeeded', finished_at = now() WHERE id = %s", (run["image_build_id"],))
    spec = wait_until(lambda: _submitted_spec(env, owner_dsn, task["id"]), timeout=60, message="the implementer never reached lux")
    assert spec["image"]["ref"] == final
    got = client.get_run(run["id"])["image"]
    assert (got["name"], got["version"], got["ref"]) == ("acme-base", 1, final)


def test_a_failed_finish_fails_the_run_before_lux(client: ApiClient, env, owner_dsn: str):
    base = _publish_first(client, owner_dsn, "slim", final=False)
    project = client.create_project(name="Slim", slug=f"slim-{os.urandom(3).hex()}", agentModels=client.on_models(SCRIPTED),
                                    runtimeImageId=base["image"]["id"])
    task = client.create_task(project["id"], "Try it")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    run = wait_until(lambda: (r := query(owner_dsn, "SELECT id, image_build_id FROM runs WHERE task_id = %s AND phase = 'implement' AND image_build_id IS NOT NULL", (task["id"],))) and r[0],
                     timeout=60, message="the implementer never waited")
    execute(owner_dsn, "UPDATE image_builds SET state = 'failed', error = 'the image needs git: agents commit with it' WHERE id = %s", (run["image_build_id"],))
    failed = wait_until(lambda: (r := client.get_run(run["id"]))["status"] == "failed" and r, timeout=60, message="the Run never failed")
    assert failed["error"] == "cannot start: its image slim v1 could not get the dude layer: the image needs git: agents commit with it"
    assert query(owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (run["id"],)) == [{"lux_run_id": None}]


def test_a_project_with_no_library_image_runs_dudes_own_as_before(client: ApiClient, env, owner_dsn: str):
    project = client.create_project(name="Plain", slug=f"plain-{os.urandom(3).hex()}", agentModels=client.on_models(SCRIPTED))
    task = client.create_task(project["id"], "Nothing new")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    spec = wait_until(lambda: _submitted_spec(env, owner_dsn, task["id"]), timeout=60, message="the implementer never reached lux")
    assert spec["image"]["ref"] == "dude-runtime:test"


def test_another_organization_cannot_see_or_use_an_image(client: ApiClient, second_org: dict, owner_dsn: str):
    mine = _publish_first(client, owner_dsn, "private")
    other: ApiClient = second_org["client"]
    assert other.get(f"/v1/images/{mine['image']['id']}").status_code == 404
    assert other.get("/v1/images/picker").json()["images"] == []
    theirs = other.create_project(name="Theirs", slug=f"theirs-{os.urandom(3).hex()}")
    refused = other.patch(f"/v1/projects/{theirs['id']}", {"runtimeImageId": mine["image"]["id"]})
    assert refused.status_code == 400 and "no image" in refused.json()["error"]["message"]


# ---------------------------------------------------------------------------
# In the browser
# ---------------------------------------------------------------------------


SHOTS = Path(os.environ["DUDE_SHOTS"]) if os.environ.get("DUDE_SHOTS") else None


def _dismiss_toasts(page: Page) -> None:
    """Closes every toast showing, so none covers what a screenshot is of."""
    toasts = page.locator("[data-toast]")
    for _ in range(toasts.count()):
        dismiss = toasts.first.get_by_role("button", name="Dismiss")
        if dismiss.count():
            dismiss.click()
    expect(toasts).to_have_count(0)


def _shoot(page: Page, name: str, *, narrow: bool = True, compact: bool = False) -> None:
    """The page at 1440 and 390 wide, light and dark, when DUDE_SHOTS is set."""
    if not SHOTS:
        return
    SHOTS.mkdir(parents=True, exist_ok=True)
    _dismiss_toasts(page)
    size = page.viewport_size or {"width": 1440, "height": 1000}
    for width in ([1440, 390] if narrow else [1440]):
        page.set_viewport_size({"width": width, "height": 1000 if width > 500 else 844})
        for theme in ("light", "dark"):
            page.evaluate("t => document.documentElement.setAttribute('data-theme', t)", theme)
            page.wait_for_timeout(150)
            page.screenshot(path=str(SHOTS / f"{name}-{width}-{theme}.png"), full_page=width > 500)
    if compact:
        page.set_viewport_size({"width": 1440, "height": 1000})
        page.evaluate("() => { document.documentElement.setAttribute('data-theme', 'light'); document.documentElement.setAttribute('data-density', 'compact'); }")
        page.wait_for_timeout(150)
        page.screenshot(path=str(SHOTS / f"{name}-1440-light-compact.png"), full_page=True)
        page.evaluate("() => document.documentElement.removeAttribute('data-density')")
    page.set_viewport_size(size)
    page.evaluate("() => document.documentElement.removeAttribute('data-theme')")


def _library(client: ApiClient, dsn: str) -> dict:
    """An organization's library as the mockup has it: a default base, two
    images FROM it (one published twice, one with a failed build), and one
    waiting in the builder's line."""
    base = _publish_first(client, dsn, "acme-base", description="Debian, Node 26, Java 25, Python 3.14, Playwright")
    client.post(f"/v1/images/default/{base['image']['id']}")
    pnpm = _publish_first(client, dsn, "node-pnpm", "# pnpm and turbo for the dashboard's agents and previews.\nFROM image:acme-base\nRUN npm install -g pnpm@9 turbo@2\nWORKDIR /workspace\n",
                          description="pnpm and turbo, for the dashboard")
    pid = pnpm["image"]["id"]
    client.put(f"/v1/images/{pid}/draft", {"containerfile": "# pnpm and turbo for the dashboard's agents and previews.\nFROM image:acme-base\nARG PNPM_VERSION=9.15.0\nRUN npm install -g pnpm@${PNPM_VERSION} turbo@2 \\\n && pnpm config set store-dir /var/cache/pnpm --global\nENV PNPM_HOME=/usr/local/share/pnpm CI=1\nWORKDIR /workspace\n", "note": "pnpm 9.15 via a build argument"})
    v2 = client.post(f"/v1/images/{pid}/build").json()
    _built(dsn, v2["versionId"])
    rails = _publish_first(client, dsn, "rails-legacy", "FROM ruby:3.1-bookworm\nRUN apt-get update && apt-get install -y nodejs\n",
                           description="Ruby 3.1 and Node 18, for the old billing app")
    rid = rails["image"]["id"]
    client.put(f"/v1/images/{rid}/draft", {"containerfile": "FROM ruby:3.1-bookworm\nRUN apt-get update && apt-get install -y nodejs\nRUN bundle install --jobs 8\n", "note": "Bundle at build time"})
    failed = client.post(f"/v1/images/{rid}/build").json()
    execute(dsn, "UPDATE image_versions SET state = 'failed', error = 'ran out of memory (1.5 GB) at step 3' WHERE id = %s", (failed["versionId"],))
    execute(dsn, """UPDATE image_builds SET state = 'failed', error = 'ran out of memory (1.5 GB) at step 3', started_at = now() - interval '3 minutes',
        finished_at = now(), build_seconds = 170
        WHERE id = %s""", (failed["buildId"],))
    _log(dsn, failed["buildId"], "build of rails-legacy v2 · podman, rootless · 1.5 CPUs · 1.5 GB memory · linux/arm64\nSTEP 1/3: FROM ruby:3.1-bookworm\nSTEP 2/3: RUN apt-get update && apt-get install -y nodejs\nSTEP 3/3: RUN bundle install --jobs 8\nInstalling nokogiri 1.16.0 with native extensions\nerror running container: exit status 137\nError: building at STEP \"RUN bundle install --jobs 8\": exit status 137")
    uv = _publish_first(client, dsn, "python-uv", "FROM image:acme-base\nRUN pip install uv\n", description="uv and the Postgres client")
    client.put(f"/v1/images/{uv['image']['id']}/draft", {"containerfile": "FROM image:acme-base\nRUN pip install uv psycopg\n", "note": "psycopg"})
    queued_uv = client.post(f"/v1/images/{uv['image']['id']}/build").json()
    # Another build running before it, with a live log.
    client.put(f"/v1/images/{pid}/draft", {"containerfile": "# pnpm and turbo for the dashboard's agents and previews.\nFROM image:acme-base\nARG PNPM_VERSION=9.15.0\nRUN npm install -g pnpm@${PNPM_VERSION} turbo@2\nWORKDIR /workspace\n", "note": "Node 26 from the base"})
    running = client.post(f"/v1/images/{pid}/build").json()
    execute(dsn, """UPDATE image_builds SET state = 'running', stage = 'building', started_at = now() - interval '130 seconds', heartbeat_at = now(),
        requested_at = now() - interval '10 minutes'
        WHERE id = %s""", (running["buildId"],))
    _log(dsn, running["buildId"], "build of node-pnpm v3 · podman, rootless · 1.5 CPUs · 1.5 GB memory · linux/arm64\nresolve image:acme-base → v1 = registry.test/dude/custom@sha256:75…\nSTEP 1/5: FROM registry.test/dude/custom@sha256:75…\nSTEP 2/5: ARG PNPM_VERSION=9.15.0\nSTEP 3/5: RUN npm install -g pnpm@9.15.0 turbo@2\nadded 2 packages in 4s")
    execute(dsn, "UPDATE image_versions SET state = 'building' WHERE id = %s", (running["versionId"],))
    return {"base": base, "pnpm": pid, "rails": rid, "uv": uv["image"]["id"], "running": running["buildId"], "failed": failed["buildId"],
            "published_build": v2["buildId"], "queued": queued_uv["buildId"]}


@pytest.mark.ui
def test_the_library_its_image_page_history_and_builds(page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, console_errors: list):
    lib = _library(client, owner_dsn)
    execute(owner_dsn, BUILDER_ALIVE)
    # The list: the queue, the tree, each image's state.
    sign_in(page, web_url, org["api_key"], at="#/org/settings/images")
    expect(page.get_by_test_id("builder-offline")).to_have_count(0)
    expect(page.get_by_test_id("build-queue")).to_contain_text("Building node-pnpm v3")
    expect(page.get_by_test_id("build-queue")).to_contain_text("1 waiting: python-uv v2")
    expect(page.get_by_test_id("images-queue-count")).to_have_text("2")
    table = page.get_by_role("table")
    rows = table.get_by_role("row").filter(has_not=page.get_by_role("columnheader"))
    expect(rows).to_have_count(4)
    expect(rows.nth(0)).to_contain_text("acme-base")
    expect(rows.nth(0)).to_contain_text("Default base")
    expect(table.get_by_role("row", name=re.compile(r"^rails-legacy"))).to_contain_text("v2 failed · v1 still live")
    expect(table.get_by_role("row", name=re.compile(r"^python-uv"))).to_contain_text("Waiting · 2nd")
    _shoot(page, "images-list", compact=True)

    # The image page: the editor, lint and autocomplete.
    table.get_by_role("row", name=re.compile(r"^node-pnpm")).click()
    expect(page.get_by_test_id("image-name")).to_have_text("node-pnpm")
    editor = page.get_by_test_id("containerfile-editor").locator(".cm-content")
    expect(editor).to_be_visible(timeout=15000)
    editor.click()
    page.keyboard.press("ControlOrMeta+End")
    page.keyboard.type("\nCOPY package.json /workspace/\nFROM image:")
    # COPY from the build context, and image: naming nothing yet.
    expect(page.get_by_test_id("lint-errors")).to_have_text("✕ 2 won’t build")
    expect(page.locator(".cm-tooltip-autocomplete")).to_contain_text("image:acme-base")
    expect(page.get_by_test_id("build-publish")).to_be_disabled()
    _shoot(page, "image-containerfile-lint-autocomplete")
    page.keyboard.press("Escape")
    # The server refuses what the editor marks, with the same words.
    page.get_by_test_id("save-draft").click()
    expect(page.get_by_test_id("image-save-problem")).to_contain_text("An image has no build files")
    # Taken out again, the draft builds: Build & publish is the page's one filled button.
    editor.click()
    page.keyboard.press("ControlOrMeta+End")
    for _ in range(len("\nCOPY package.json /workspace/\nFROM image:")):
        page.keyboard.press("Backspace")
    page.keyboard.type("\nRUN pnpm --version")
    expect(page.get_by_test_id("lint-errors")).to_have_count(0)
    publish = page.get_by_test_id("build-publish")
    expect(publish).to_be_enabled()
    # The button eases from its disabled wash to its fill: wait for the fill
    # rather than reading the colour once, mid-transition.
    save_bg = page.get_by_test_id("save-draft").evaluate("b => getComputedStyle(b).backgroundColor")
    expect(publish).not_to_have_css("background-color", save_bg)
    _shoot(page, "image-containerfile-ready")

    # History: a diff, and Publish again.
    page.goto(f"{web_url}#/org/settings/images/{lib['pnpm']}/history")
    history = page.get_by_test_id("image-history")
    history.locator("[data-version='1']").click()
    expect(history).to_contain_text("v1, whole")
    history.get_by_role("button", name="With published").click()
    expect(history).to_contain_text("v2 (published) → v1")
    history.get_by_role("button", name="Publish v1 again").click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("Publish v1 again?")
    expect(dialog).to_contain_text("The v3 build that is running carries on")
    _shoot(page, "image-history-republish")
    page.get_by_test_id("republish-confirm").click()
    wait_until(lambda: client.get(f"/v1/images/{lib['pnpm']}").json()["image"]["published"]["number"] == 1, timeout=10,
               message="v1 was not published again")

    # Builds: running, failed, published.
    page.goto(f"{web_url}#/org/settings/images/builds/{lib['running']}")
    expect(page.get_by_test_id("build-stages")).to_have_attribute("data-state", "running")
    expect(page.get_by_test_id("build-log")).to_contain_text("STEP 3/5")
    _shoot(page, "build-running")
    page.goto(f"{web_url}#/org/settings/images/builds/{lib['failed']}")
    expect(page.get_by_test_id("build-error")).to_have_text("ran out of memory (1.5 GB) at step 3")
    _shoot(page, "build-failed")
    page.goto(f"{web_url}#/org/settings/images/builds/{lib['published_build']}")
    expect(page.get_by_test_id("build-stages")).to_have_attribute("data-state", "succeeded")
    _shoot(page, "build-published")
    # The one refusal is the draft the server would not save, on purpose above.
    assert [e for e in console_errors if "422" not in e] == [] and len(console_errors) <= 1, console_errors


@pytest.mark.ui
def test_an_offline_builder_is_said_on_the_list_and_on_a_waiting_run(page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str,
                                                                      console_errors: list):
    base = _publish_first(client, owner_dsn, "acme-base", final=False)
    client.post(f"/v1/images/default/{base['image']['id']}")
    execute(owner_dsn, "INSERT INTO image_builder (seen_at) VALUES ('2026-10-01T08:00:00Z') ON CONFLICT (id) DO UPDATE SET seen_at = EXCLUDED.seen_at")
    # image_builder is one row the whole session shares: alive again whatever happens here.
    try:
        listed = client.get("/v1/images").json()["builder"]
        assert listed["offline"] is True
        project = client.create_project(name="Greeter", slug=f"greeter-{os.urandom(3).hex()}", agentModels=client.on_models(SCRIPTED))
        task = client.create_task(project["id"], "Say hello")
        assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
        run = wait_until(lambda: (r := query(owner_dsn, "SELECT id FROM runs WHERE task_id = %s AND image_build_id IS NOT NULL", (task["id"],))) and r[0],
                         timeout=60, message="the implementer never waited on its image")
        assert client.get_run(run["id"])["preparingImage"]["builderOfflineSince"] is not None
        sign_in(page, web_url, org["api_key"], at="#/org/settings/images")
        expect(page.get_by_test_id("builder-offline")).to_contain_text("Image builder offline since")
        _shoot(page, "images-builder-offline")
        page.goto(f"{web_url}#/session/{run['id']}")
        expect(page.get_by_test_id("preparing-image")).to_contain_text("image builder offline since")
        _shoot(page, "run-preparing-image-offline")
    finally:
        execute(owner_dsn, BUILDER_ALIVE)
    assert all("409" in e for e in console_errors), console_errors


@pytest.mark.ui
def test_a_member_reads_the_library_and_changes_nothing(page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, env, console_errors: list):
    _publish_first(client, owner_dsn, "acme-base")
    member = client.post("/v1/people", {"name": "Bo", "email": f"bo-{os.urandom(2).hex()}@acme.dev", "role": "member"}).json()["key"]
    sign_in(page, web_url, member, at="#/org/settings/images")
    rows = page.get_by_role("table").get_by_role("row", name=re.compile(r"^acme-base"))
    expect(rows).to_have_count(1)
    expect(page.get_by_test_id("new-image")).to_have_count(0)
    rows.click()
    expect(page.get_by_test_id("containerfile-editor").locator(".cm-content")).to_have_attribute("contenteditable", "false", timeout=15000)
    expect(page.get_by_test_id("build-publish")).to_have_count(0)
    assert console_errors == []


@pytest.mark.ui
def test_the_picker_on_a_project_and_a_role_stores_the_image_and_a_typed_one_is_cleared(
    page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, console_errors: list
):
    lib = _library(client, owner_dsn)
    execute(owner_dsn, BUILDER_ALIVE)
    project = client.create_project(name="Dashboard", slug=f"dash-{os.urandom(3).hex()}", agentModels=client.on_models(SCRIPTED))
    # A typed image from before the library.
    execute(owner_dsn, "UPDATE projects SET runtime_image = 'ghcr.io/acme/runner:node22' WHERE id = %s", (project["id"],))
    sign_in(page, web_url, org["api_key"], at=f"#/project/{project['id']}/settings/general")
    field = page.get_by_test_id("runtime-image")
    expect(page.get_by_test_id("runtime-image-legacy")).to_contain_text("ghcr.io/acme/runner:node22")
    expect(page.get_by_test_id("runtime-image-legacy")).to_contain_text("acme-base v1 wins over it")
    combo = field.get_by_role("combobox")
    combo.click()
    combo.fill("py")
    options = field.get_by_role("option")
    expect(options).to_have_count(2)
    expect(options.nth(1)).to_contain_text("v2 waiting")
    _shoot(page, "picker-project")
    combo.press("ArrowDown")
    combo.press("Enter")
    wait_until(lambda: client.get(f"/v1/projects/{project['id']}").json()["runtimeImageId"] == lib["uv"], timeout=10,
               message="the project's runtime image was not saved")
    expect(combo).to_have_value("python-uv")
    page.get_by_test_id("runtime-image-clear-legacy").click()
    wait_until(lambda: client.get(f"/v1/projects/{project['id']}").json()["runtimeImage"] is None, timeout=10,
               message="the typed image was not cleared")
    expect(page.get_by_test_id("runtime-image-legacy")).to_have_count(0)

    # An agent role on the organisation: the tester runs in node-pnpm.
    page.goto(f"{web_url}#/org/settings/qa_browser")
    role = page.get_by_test_id("role-image")
    role.get_by_role("combobox").click()
    role.get_by_role("option").filter(has_text="node-pnpm").click()
    wait_until(lambda: client.get("/v1/settings/organization").json()["roles"]["qa_browser"]["image"]["value"] == lib["pnpm"], timeout=10,
               message="the tester's image was not saved")
    role.get_by_role("combobox").click()
    _shoot(page, "picker-role")
    role.get_by_role("combobox").press("Escape")
    # On the project, the tester's image says it is the organisation's.
    page.goto(f"{web_url}#/project/{project['id']}/settings/qa_browser")
    expect(page.get_by_test_id("role-image-row")).to_contain_text(re.compile(r"From .*"))
    expect(page.get_by_test_id("role-image").get_by_role("combobox")).to_have_attribute("aria-expanded", "false")
    assert console_errors == []


@pytest.mark.ui
def test_a_run_preparing_its_image_says_so(page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, console_errors: list):
    base = _publish_first(client, owner_dsn, "acme-base", final=False)
    client.post(f"/v1/images/default/{base['image']['id']}")
    execute(owner_dsn, BUILDER_ALIVE)
    project = client.create_project(name="Greeter", slug=f"greeter-{os.urandom(3).hex()}", agentModels=client.on_models(SCRIPTED))
    task = client.create_task(project["id"], "Say hello")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    run = wait_until(lambda: (r := query(owner_dsn, "SELECT id FROM runs WHERE task_id = %s AND image_build_id IS NOT NULL", (task["id"],))) and r[0],
                     timeout=60, message="the implementer never waited on its image")
    refused: list[str] = []
    page.on("response", lambda r: refused.append(f"{r.status} {r.url}") if r.status >= 400 else None)
    sign_in(page, web_url, org["api_key"], at=f"#/session/{run['id']}")
    expect(page.get_by_test_id("preparing-image")).to_contain_text("Preparing image: adding the dude layer")
    expect(page.get_by_test_id("preparing-image")).to_contain_text("acme-base v1")
    _shoot(page, "run-preparing-image")
    # A Run on an image whose first version is still building waits on that build, and says so.
    first = _image(client, "node-first")
    assert client.post(f"/v1/images/{first['image']['id']}/build").status_code == 201
    other = client.create_project(name="Fresh", slug=f"fresh-{os.urandom(3).hex()}", agentModels=client.on_models(SCRIPTED), runtimeImageId=first["image"]["id"])
    task2 = client.create_task(other["id"], "Say hello")
    assert client.post(f"/v1/tasks/{task2['id']}/deliver").status_code == 201
    run2 = wait_until(lambda: (r := query(owner_dsn, "SELECT id FROM runs WHERE task_id = %s AND image_build_id IS NOT NULL", (task2["id"],))) and r[0],
                      timeout=60, message="the implementer never waited on the first build")
    page.goto(f"{web_url}#/session/{run2['id']}")
    expect(page.get_by_test_id("preparing-image")).to_contain_text("Preparing image: building node-first v1 (its first version)")
    expect(page.get_by_test_id("preparing-image")).not_to_contain_text("a minute or two")
    _shoot(page, "run-preparing-image-build")
    # An organization with no GitHub answers 409 for its forge settings, which
    # the session page reads: not this page's doing.
    assert [r for r in refused if "/v1/forge/settings" not in r] == []
    assert all("409" in e for e in console_errors), console_errors
