"""Can run containers: a property of a library image version, through the
public API and the built UI, against the deployed backend, orchestrator and
fake lux (started with -no-nested-host: no host runs nested containers, so a
Run that asks for them waits, saying why).

dude-image-builder's part (a build, its container check) is played by its
own statements, as in test_images.py. With DUDE_SHOTS=<dir> set, the
browser tests save the pages they reach, light and dark.
"""

from __future__ import annotations

import os
from pathlib import Path

import pytest
import requests
from playwright.sync_api import Page, expect

from env import TEST_LAYER
from helpers import ApiClient, execute, query, sign_in, wait_until
from suites.test_images import BUILDER_ALIVE, SCRIPTED, _built, _dismiss_toasts, _log, _publish_first

PODMAN = ("FROM debian:bookworm-slim\nRUN apt-get update && apt-get install -y --no-install-recommends \\\n"
          "    git podman fuse-overlayfs uidmap \\\n && rm -rf /var/lib/apt/lists/*\n")
NODE = "FROM node:22-bookworm-slim\nRUN apt-get update && apt-get install -y git\nRUN corepack enable\n"
SHOTS = Path(os.environ["DUDE_SHOTS"]) if os.environ.get("DUDE_SHOTS") else None


def _shoot(page: Page, name: str) -> None:
    if not SHOTS:
        return
    SHOTS.mkdir(parents=True, exist_ok=True)
    _dismiss_toasts(page)
    size = page.viewport_size
    # The app scrolls inside its panes: a tall viewport, not a full-page shot.
    page.set_viewport_size({"width": 1440, "height": 1300})
    page.wait_for_timeout(200)
    for theme in ("light", "dark"):
        page.evaluate("t => document.documentElement.setAttribute('data-theme', t)", theme)
        page.wait_for_timeout(200)
        page.screenshot(path=str(SHOTS / f"{name}-{theme}.png"))
    page.evaluate("() => document.documentElement.removeAttribute('data-theme')")
    if size:
        page.set_viewport_size(size)


def _able(client: ApiClient, dsn: str, name: str = "agents-podman", containerfile: str = PODMAN, **body) -> dict:
    return _publish_first(client, dsn, name, containerfile, canRunContainers=True, **body)


def _lux_spec(env, lux_run_id: str) -> dict:
    return requests.get(f"{env.fake_lux_url}/v1/runs/{lux_run_id}", headers={"authorization": f"Bearer {env.lux_key}"}, timeout=10).json()


# ---------------------------------------------------------------------------
# Through the API
# ---------------------------------------------------------------------------


def test_an_agent_run_on_an_image_that_can_asks_lux_and_waits_saying_why(client: ApiClient, env, owner_dsn: str):
    image = _able(client, owner_dsn)
    project = client.create_project(name="Greeter", slug=f"greeter-{os.urandom(3).hex()}", agentModels=client.on_models(SCRIPTED),
                                    runtimeImageId=image["image"]["id"])
    task = client.create_task(project["id"], "Say hello")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    run = wait_until(lambda: (r := query(owner_dsn, "SELECT id, lux_run_id, image FROM runs WHERE task_id = %s AND phase = 'implement' AND lux_run_id IS NOT NULL",
                                         (task["id"],))) and r[0], timeout=60, message="the implementer never reached lux")
    assert run["image"]["canRunContainers"] is True
    lux = _lux_spec(env, run["lux_run_id"])
    assert lux["spec"]["sandbox"] == {"nestedContainers": True}
    # No host runs nested containers: the Run waits, and its servers' view says why.
    servers = wait_until(lambda: (s := client.get(f"/v1/runs/{run['id']}/servers").json())["run"].get("waitingReason") and s,
                         timeout=30, message="no waiting reason")
    assert servers["run"]["waitingReason"] == "waiting for capacity: 1 host in its pool does not support nested containers"


def test_a_preview_on_an_image_that_can_keeps_its_engine_store(client: ApiClient, env, owner_dsn: str, forge_project: dict):
    image = _able(client, owner_dsn, "abs-preview")
    pid = forge_project["id"]
    assert client.put(f"/v1/projects/{pid}/preview-settings", {"imageId": image["image"]["id"]}).status_code == 200
    task = client.create_task(pid, "Pay with saved cards")
    assert client.post(f"/v1/tasks/{task['id']}/preview").status_code == 201
    run = wait_until(lambda: (r := query(owner_dsn, "SELECT id, lux_run_id FROM runs WHERE task_id = %s AND kind = 'preview' AND lux_run_id IS NOT NULL",
                                         (task["id"],))) and r[0], timeout=60, message="the preview never reached lux")
    spec = _lux_spec(env, run["lux_run_id"])["spec"]
    assert spec["sandbox"] == {"nestedContainers": True}
    assert {"name": "engines", "path": "/home/agent/.local/share", "kind": "state"} in spec["volumes"]
    assert spec["env"]["XDG_DATA_HOME"] == "/home/agent/.local/share"


# ---------------------------------------------------------------------------
# In the browser
# ---------------------------------------------------------------------------


def _library(client: ApiClient, dsn: str) -> dict:
    """The mockup's library: agents-podman and abs-preview FROM it, both
    able; node-22 that cannot; and abs-preview with a draft v2."""
    podman = _able(client, dsn, description="Debian, git and rootless podman")
    abs_preview = _publish_first(client, dsn, "abs-preview", "FROM image:agents-podman\nUSER root\nRUN apt-get update\n",
                                 description="The abs test stack: Python, uv, Java 25, Postgres and Kafka in podman compose.")
    node = _publish_first(client, dsn, "node-22", NODE, description="Node 22 and pnpm, for the web apps.")
    return {"podman": podman["image"]["id"], "abs": abs_preview["image"]["id"], "node": node["image"]["id"]}


@pytest.mark.ui
def test_the_library_says_which_images_can_run_containers_and_the_box_is_saved_with_the_draft(
        page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, console_errors: list):
    lib = _library(client, owner_dsn)
    execute(owner_dsn, BUILDER_ALIVE)
    sign_in(page, web_url, org["api_key"], at="#/org/settings/images")
    table = page.get_by_test_id("images-table")
    expect(table.locator("[data-image='agents-podman']").get_by_test_id("can-run-containers")).to_have_text("Can run containers")
    expect(table.locator("[data-image='abs-preview']").get_by_test_id("can-run-containers")).to_have_count(1)
    expect(table.locator("[data-image='node-22']").get_by_test_id("can-run-containers")).to_have_count(0)
    _shoot(page, "01-images-list")

    # abs-preview, inherited from agents-podman: on, badge in the header, aside says so.
    page.goto(f"{web_url}#/org/settings/images/{lib['abs']}")
    expect(page.get_by_test_id("image-name")).to_have_text("abs-preview")
    expect(page.get_by_test_id("image-fact-containers")).to_have_text("Can run them")
    header = page.locator(".imageHeadActions")
    expect(header.get_by_test_id("can-run-containers")).to_have_text("Can run containers")
    field = page.get_by_test_id("can-run-containers-field")
    expect(field.get_by_role("checkbox")).to_be_checked()
    expect(page.get_by_test_id("containers-hint")).to_have_count(0)
    _shoot(page, "02-image-containers")

    # node-22: off. Ticked, the footer says so, and with no podman anywhere, the hint.
    page.goto(f"{web_url}#/org/settings/images/{lib['node']}")
    expect(page.get_by_test_id("image-fact-containers")).to_have_text("No")
    expect(page.locator(".imageHeadActions").get_by_test_id("can-run-containers")).to_have_count(0)
    box = page.get_by_test_id("can-run-containers-field").get_by_role("checkbox")
    expect(box).not_to_be_checked()
    _shoot(page, "02b-image-off")
    box.click()
    expect(page.get_by_test_id("containers-changed")).to_have_text("Can run containers turned on")
    expect(page.get_by_test_id("containers-hint")).to_have_text(
        "This Containerfile doesn’t install podman or Docker. The build will fail its container check unless the base has them.")
    page.get_by_test_id("image-note").fill("Run containers for the e2e suite")
    _shoot(page, "02c-image-on-hint")
    page.get_by_test_id("save-draft").click()
    wait_until(lambda: client.get(f"/v1/images/{lib['node']}").json()["versions"][0]["canRunContainers"] is True, timeout=10,
               message="the draft did not keep the box")
    # Saved: the published version still cannot, so the badges stay off.
    assert client.get(f"/v1/images/{lib['node']}").json()["image"]["published"]["canRunContainers"] is False
    assert console_errors == []


@pytest.mark.ui
def test_turning_it_off_where_previews_use_it_warns_they_lose_their_containers(
        page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, console_errors: list):
    lib = _library(client, owner_dsn)
    project = client.create_project(name="abs", slug=f"abs-{os.urandom(3).hex()}")
    client.put(f"/v1/projects/{project['id']}/preview-settings", {"imageId": lib["abs"]})
    sign_in(page, web_url, org["api_key"], at=f"#/org/settings/images/{lib['abs']}")
    box = page.get_by_test_id("can-run-containers-field").get_by_role("checkbox")
    expect(box).to_be_checked()
    expect(page.get_by_test_id("containers-off-warning")).to_have_count(0)
    box.click()
    expect(page.get_by_test_id("containers-off-warning")).to_have_text("Previews of this image lose their saved containers on their next wake.")
    expect(page.get_by_test_id("containers-changed")).to_have_text("Can run containers turned off")
    _shoot(page, "02d-image-off-warning")
    # agents-podman is named by no preview: no warning.
    page.goto(f"{web_url}#/org/settings/images/{lib['podman']}")
    page.get_by_test_id("can-run-containers-field").get_by_role("checkbox").click()
    expect(page.get_by_test_id("containers-changed")).to_have_text("Can run containers turned off")
    expect(page.get_by_test_id("containers-off-warning")).to_have_count(0)
    assert console_errors == []


@pytest.mark.ui
def test_history_says_when_it_was_turned_on(page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, console_errors: list):
    node = _publish_first(client, owner_dsn, "node-22", NODE)
    nid = node["image"]["id"]
    client.put(f"/v1/images/{nid}/draft", {"containerfile": NODE + "RUN apt-get install -y podman\n", "note": "Run podman for the tests",
                                          "canRunContainers": True})
    v2 = client.post(f"/v1/images/{nid}/build").json()
    _built(owner_dsn, v2["versionId"])
    sign_in(page, web_url, org["api_key"], at=f"#/org/settings/images/{nid}/history")
    history = page.get_by_test_id("image-history")
    v2_item = history.locator("[data-version='2']")
    expect(v2_item.get_by_test_id("version-flag")).to_have_text("Can run containers turned on")
    expect(history.locator("[data-version='1']").get_by_test_id("version-flag")).to_have_count(0)
    v2_item.click()
    expect(history.get_by_test_id("flag-diff")).to_have_text("Can run containers off → on")
    _shoot(page, "03-history")
    # v1, whole: the value alone.
    history.locator("[data-version='1']").click()
    expect(history.get_by_test_id("flag-diff")).to_have_text("Can run containers off")
    assert console_errors == []


@pytest.mark.ui
def test_a_build_page_shows_the_container_check_passing_and_failing(
        page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, console_errors: list):
    able = _able(client, owner_dsn, "agents-podman")
    node = _publish_first(client, owner_dsn, "node-22", NODE)
    # node-22 v2 marked, and its check failed: what the builder writes.
    nid = node["image"]["id"]
    client.put(f"/v1/images/{nid}/draft", {"containerfile": NODE + "RUN true\n", "note": "Run containers for the e2e suite", "canRunContainers": True})
    failed = client.post(f"/v1/images/{nid}/build").json()
    sentence = "Can't run containers: the image has no podman or rootless Docker, no fuse-overlayfs, and no newuidmap or newgidmap."
    execute(owner_dsn, "UPDATE image_versions SET state = 'failed', error = %s, user_ref = 'registry.test/dude/custom@sha256:' || repeat('a', 64) WHERE id = %s",
            (sentence, failed["versionId"]))
    execute(owner_dsn, """UPDATE image_builds SET state = 'failed', error = %s, started_at = now() - interval '3 minutes', finished_at = now(),
        build_seconds = 178, check_seconds = 6,
        containers_check = '{"passed": false, "detail": "Missing: podman or Docker, fuse-overlayfs, newuidmap, newgidmap"}' WHERE id = %s""",
            (sentence, failed["buildId"]))
    _log(owner_dsn, failed["buildId"], "STEP 3/3: RUN true\nCheck containers: v2 is marked Can run containers, so dude checks it.\n"
         "check  engine  podman: not found · docker: not found\ncheck  fuse-overlayfs  not found\ncheck  newuidmap  missing\n"
         "check  newgidmap  missing\ncheck  subuid  agent:1:999, agent:1001:64535\ncheck  subgid  agent:1:999, agent:1001:64535\n"
         f"{sentence}\nNot pushed. v1 stays published.\n")
    sign_in(page, web_url, org["api_key"], at=f"#/org/settings/images/builds/{failed['buildId']}")
    stages = page.get_by_test_id("build-stages")
    expect(stages).to_contain_text("Check containers")
    expect(stages).to_contain_text("Missing: podman or Docker, fuse-overlayfs, newuidmap, newgidmap")
    expect(stages).to_contain_text("Not pushed · v1 is still live")
    expect(page.get_by_test_id("build-error")).to_have_text(sentence)
    expect(page.get_by_test_id("build-fix")).to_contain_text("FROM image:agents-podman")
    expect(page.get_by_test_id("build-log")).to_contain_text("Not pushed. v1 stays published.")
    _shoot(page, "04-build-failed")

    # agents-podman's own v1, checked and passed.
    passed = query(owner_dsn, "SELECT id FROM image_builds WHERE image_version_id = %s", (able["image"]["published"]["versionId"],))[0]["id"]
    execute(owner_dsn, """UPDATE image_builds SET check_seconds = 4,
        containers_check = '{"passed": true, "detail": "podman 5.4, fuse-overlayfs, newuidmap/newgidmap with capabilities, subuid for agent"}' WHERE id = %s""",
            (passed,))
    _log(owner_dsn, passed, "Check containers: v1 is marked Can run containers, so dude checks it.\ncheck  engine  podman: /usr/bin/podman (5.4) · docker: not found\n"
         "Check passed: it can run containers.\n")
    page.goto(f"{web_url}#/org/settings/images/builds/{passed}")
    expect(page.get_by_test_id("build-stages")).to_contain_text("podman 5.4, fuse-overlayfs, newuidmap/newgidmap with capabilities, subuid for agent")
    expect(page.get_by_text("checking containers")).to_be_visible()
    _shoot(page, "04b-build-passed")
    assert console_errors == []


@pytest.mark.ui
def test_pickers_say_an_image_can_run_containers_and_a_typed_one_cannot(
        page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, console_errors: list):
    lib = _library(client, owner_dsn)
    project = client.create_project(name="abs", slug=f"abs-{os.urandom(3).hex()}")
    execute(owner_dsn, "UPDATE projects SET runtime_image = 'ghcr.io/acme/agent:1.4' WHERE id = %s", (project["id"],))
    sign_in(page, web_url, org["api_key"], at=f"#/project/{project['id']}/settings/general")
    expect(page.get_by_test_id("runtime-image-legacy")).to_contain_text("It can’t run containers.")
    _shoot(page, "07-typed-by-hand")
    field = page.get_by_test_id("runtime-image")
    field.get_by_role("combobox").click()
    option = field.get_by_role("option").filter(has_text="agents-podman")
    expect(option.get_by_test_id("can-run-containers")).to_have_text("can run containers")
    expect(field.get_by_role("option").filter(has_text="node-22").get_by_test_id("can-run-containers")).to_have_count(0)
    _shoot(page, "05-picker-open")
    option.click()
    expect(page.get_by_test_id("runtime-image-containers")).to_have_text(
        "This image can run containers: this project’s agents can start containers.")
    _shoot(page, "05b-picker-picked")

    page.goto(f"{web_url}#/project/{project['id']}/settings/qa_browser")
    role = page.get_by_test_id("role-image")
    role.get_by_role("combobox").click()
    role.get_by_role("option").filter(has_text="agents-podman").click()
    expect(page.get_by_test_id("role-image-containers")).to_have_text("This image can run containers: testers can start containers.")
    _shoot(page, "06-role-image")

    # Branch previews: the note under the Image row, only for an image that can.
    page.goto(f"{web_url}#/project/{project['id']}/settings/servers")
    preview = page.get_by_test_id("preview-image")
    preview.get_by_role("combobox").click()
    # Each pick is saved at once; its toast is awaited so a shot's dismissal finds it.
    saved = page.locator("[data-toast]").filter(has_text="Image saved")
    preview.get_by_role("option").filter(has_text="abs-preview").click()
    expect(page.get_by_test_id("preview-containers-note")).to_contain_text(
        "This image can run containers. A preview keeps its containers while it sleeps, so it wakes in seconds instead of rebuilding them.")
    expect(saved).to_have_count(1)
    _shoot(page, "08-previews-containers")
    _dismiss_toasts(page)
    preview.get_by_role("combobox").click()
    preview.get_by_role("option").filter(has_text="node-22").click()
    expect(page.get_by_test_id("preview-containers-note")).to_have_count(0)
    expect(saved).to_have_count(1)
    _shoot(page, "08b-previews-plain")
    assert console_errors == []


@pytest.mark.ui
def test_a_preview_and_a_run_waiting_for_a_host_that_can_run_containers_say_so(
        page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, forge_project: dict, console_errors: list):
    image = _able(client, owner_dsn, "abs-preview")
    pid = forge_project["id"]
    client.put(f"/v1/projects/{pid}/preview-settings", {"imageId": image["image"]["id"]})
    task = client.create_task(pid, "Pay with saved cards")
    assert client.post(f"/v1/tasks/{task['id']}/preview").status_code == 201
    run = wait_until(lambda: (r := query(owner_dsn, "SELECT id FROM runs WHERE task_id = %s AND kind = 'preview' AND lux_run_id IS NOT NULL",
                                         (task["id"],))) and r[0], timeout=60, message="the preview never reached lux")
    sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}/servers")
    waiting = page.get_by_test_id("waiting-for-host")
    expect(waiting).to_have_text("Waiting for a host that can run containers. lux has no host that can run containers. An admin can add one to a pool.")
    expect(page.get_by_test_id("run-waiting")).to_have_text("Waiting")
    _shoot(page, "09-waiting-preview")

    # An agent Run on it: its page says the same, pinned, and its header.
    agent_task = client.create_task(pid, "Test saved cards")
    client.patch(f"/v1/projects/{pid}", {"runtimeImageId": image["image"]["id"]})
    assert client.post(f"/v1/tasks/{agent_task['id']}/deliver").status_code == 201
    agent = wait_until(lambda: (r := query(owner_dsn, "SELECT id FROM runs WHERE task_id = %s AND phase = 'implement' AND lux_run_id IS NOT NULL",
                                           (agent_task["id"],))) and r[0], timeout=60, message="the implementer never reached lux")
    page.goto(f"{web_url}#/session/{agent['id']}")
    pinned = page.get_by_test_id("waiting-for-host")
    expect(pinned).to_contain_text("Waiting for a host that can run containers.")
    expect(pinned).to_contain_text("Nothing is spent meanwhile.")
    expect(page.get_by_test_id("run-waiting")).to_have_text("Waiting for a host")
    expect(page.get_by_test_id("run-image")).to_contain_text("abs-preview")
    _shoot(page, "09b-waiting-run")
    assert all("409" in e for e in console_errors), console_errors
