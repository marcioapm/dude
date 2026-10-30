"""The task cockpit's whole-branch Changes, against the real stack.

A delivery's agents push the task branch to the fake GitHub and open a pull
request; the orchestrator's branch-snapshots loop captures the branch as
GitHub's compare reads it (merge base…head), and `GET /v1/tasks/:id/branch-diff`
serves the snapshot. Moves of the branch on GitHub — the target merged in, a
force-push, one more commit — are new snapshots in one lineage, each saying
how it relates to the one before. In the web app a task opens on its
Overview, whose Changes card leads to the Changes tab: the snapshot pinned
while newer ones are announced, general feedback written from its bar.

The fake GitHub's compare patches carry hunk headers only (no lines), so
nothing here comments on a line.
"""

from __future__ import annotations

import re
import subprocess

from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient, sign_in, wait_until


def _branch_diff(client: ApiClient, task_id: str, **params) -> dict:
    resp = client.get(f"/v1/tasks/{task_id}/branch-diff", params=params)
    assert resp.status_code == 200, resp.text
    return resp.json()


def _delivered(client: ApiClient, project: dict, fake_github: FakeGitHub, title: str) -> tuple[dict, dict]:
    """A delivered task in review: its pull request open on the fake GitHub,
    and dude told only (never updating the branch itself) when main moves."""
    assert client.patch("/v1/forge/settings", {"whenBehind": "tell"}).status_code == 200
    task = client.create_task(project["id"], title)
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
                    timeout=90, message="no pull request opened")[0]
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=60,
               message="the task never reached review")
    return task, pr


def _captured(client: ApiClient, task_id: str, head: str, message: str) -> dict:
    """The branch diff once its latest snapshot is at `head`."""
    def at_head():
        d = _branch_diff(client, task_id)
        return d if d["availability"]["state"] == "ok" and d["snapshot"]["headSha"] == head else None

    return wait_until(at_head, timeout=60, interval=0.5, message=message)


def _changed(fake_github: FakeGitHub, base: str, head: str) -> dict[str, tuple[int, int]]:
    """The branch's own changes, from git: each path's additions and deletions since the merge base."""
    out = fake_github._git_out("diff", "--no-renames", "--numstat", base, head)
    return {path: (int(a), int(d)) for a, d, path in (line.split("\t", 2) for line in out.splitlines())}


def _files(snapshot: dict) -> dict[str, tuple[int, int]]:
    return {f["path"]: (f["additions"], f["deletions"]) for f in snapshot["files"]}


def _synchronize(fake_github: FakeGitHub, number: int) -> None:
    """GitHub tells dude the pull request's branch moved."""
    fake_github.send_webhook("pull_request", {"action": "synchronize", "pull_request": {"number": number}})


def _amend(fake_github: FakeGitHub, branch: str, message: str) -> str:
    """A person amends the branch's head commit (same tree and parents, another
    message) and force-pushes it: the old head is no longer on the branch."""
    head = fake_github.branch_sha(branch)
    parents = fake_github._git_out("rev-list", "--parents", "-n", "1", head).split()[1:]
    args = ["commit-tree", f"{head}^{{tree}}"]
    for p in parents:
        args += ["-p", p]
    sha = fake_github._git_out(*args, "-m", message,
                               env={"GIT_AUTHOR_NAME": "Gus Human", "GIT_AUTHOR_EMAIL": "gus@example.com",
                                    "GIT_COMMITTER_NAME": "Gus Human", "GIT_COMMITTER_EMAIL": "gus@example.com"})
    fake_github._git_out("update-ref", f"refs/heads/{branch}", sha, head)
    return sha


def _merge_target_in(fake_github: FakeGitHub, branch: str, target: str = "main") -> str:
    """A person merges the target into the branch on GitHub (Update branch's merge commit): its sha."""
    head, base = fake_github.branch_sha(branch), fake_github.branch_sha(target)
    assert head and base, (branch, target)
    tree = fake_github._git_out("merge-tree", "--write-tree", head, base).split("\n", 1)[0]
    sha = fake_github._git_out("commit-tree", tree, "-p", head, "-p", base, "-m", f"Merge branch '{target}' into {branch}",
                               env={"GIT_AUTHOR_NAME": "Gus Human", "GIT_AUTHOR_EMAIL": "gus@example.com",
                                    "GIT_COMMITTER_NAME": "Gus Human", "GIT_COMMITTER_EMAIL": "gus@example.com"})
    fake_github._git_out("update-ref", f"refs/heads/{branch}", sha, head)
    return sha


def test_the_branch_is_captured_and_each_move_is_a_snapshot_in_its_lineage(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    task, pr = _delivered(client, forge_project, fake_github, "Greet by name")
    branch = pr["headBranch"]
    head = fake_github.branch_sha(branch)

    # Captured as GitHub's compare reads it: main's merge base … the branch head.
    first = _captured(client, task["id"], head, "the pushed branch was never captured")
    snap = first["snapshot"]
    assert first["availability"] == {"state": "ok"} and first["repository"] == "greeter"
    assert snap["targetRef"] == "main" and snap["branchRef"] == branch
    assert snap["mergeBaseSha"] == fake_github._git_out("merge-base", "main", head)
    assert snap["headSha"] == head
    # The delivery pushed more than once (implementer, simplifier): walked back,
    # the lineage starts with the initial snapshot, each push after it a fast-forward.
    lineage = [snap]
    while lineage[-1]["previousSnapshotId"]:
        lineage.append(_branch_diff(client, task["id"], snapshot=lineage[-1]["previousSnapshotId"])["snapshot"])
    assert [s["relation"] for s in lineage] == ["fast-forward"] * (len(lineage) - 1) + ["initial"], lineage
    root = lineage[-1]
    assert root["targetRef"] == "main" and root["mergeBaseSha"] == snap["mergeBaseSha"]
    assert root["headSha"] in fake_github._git_out("rev-list", head).split()
    changed = _changed(fake_github, snap["mergeBaseSha"], head)
    assert changed, "the delivery pushed no change"
    assert _files(snap) == changed
    assert snap["stats"]["files"] == len(changed)
    assert first["newer"] is None

    # main moves (a file of its own), then is merged into the branch: the
    # merge base is main's new head, and the files are still only the branch's.
    main_head = fake_github.commit_file("main", "MAIN_ONLY.md", "Someone else's work\n", "Someone else's work")
    merged = _merge_target_in(fake_github, branch)
    _synchronize(fake_github, pr["number"])
    second = _captured(client, task["id"], merged, "the merge of main into the branch was never captured")["snapshot"]
    assert second["relation"] == "merged-target", second
    assert second["previousSnapshotId"] == snap["id"]
    assert second["mergeBaseSha"] == main_head != snap["mergeBaseSha"]
    assert _files(second) == changed and "MAIN_ONLY.md" not in _files(second)
    # Read at the first snapshot, the newer one says what happened since.
    older = _branch_diff(client, task["id"], snapshot=snap["id"])
    assert older["snapshot"]["id"] == snap["id"]
    newer = older["newer"]
    assert newer["id"] == second["id"] and newer["relation"] == "merged-target", newer
    assert (newer["fromMergeBaseSha"], newer["mergeBaseSha"]) == (snap["mergeBaseSha"], main_head)
    assert (newer["fromHeadSha"], newer["headSha"], newer["count"]) == (head, merged, 1)
    assert _branch_diff(client, task["id"], snapshot=root["id"])["newer"]["relation"] == "merged-target"

    # Amended and force-pushed: the old head is not on the new branch.
    rewritten = _amend(fake_github, branch, "Merge main, reworded")
    _synchronize(fake_github, pr["number"])
    third = _captured(client, task["id"], rewritten, "the force-push was never captured")["snapshot"]
    assert third["relation"] == "rewritten", third
    assert third["previousSnapshotId"] == second["id"]
    assert third["mergeBaseSha"] == main_head
    assert _files(third) == changed
    assert _branch_diff(client, task["id"], snapshot=snap["id"])["newer"]["relation"] == "rewritten"
    # From the initial snapshot, too: the span crosses the rewrite.
    assert _branch_diff(client, task["id"], snapshot=root["id"])["newer"]["relation"] == "rewritten"


def _feedback(client: ApiClient, task_id: str) -> list[dict]:
    return client.get(f"/v1/tasks/{task_id}/feedback").json()["feedback"]


def test_a_task_opens_on_its_overview_whose_changes_lead_to_the_branch_and_its_general_feedback(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github: FakeGitHub, console_errors: list
):
    task, pr = _delivered(client, forge_project, fake_github, "Review the branch")
    snap = _captured(client, task["id"], fake_github.branch_sha(pr["headBranch"]), "the branch was never captured")["snapshot"]
    count = snap["stats"]["files"]
    words = f"{count} file" + ("" if count == 1 else "s")

    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    tabs = page.get_by_role("tablist", name="Task")
    # Overview, not Chat: the task's page opens on it.
    expect(tabs.get_by_role("tab", name="Overview")).to_have_attribute("aria-selected", "true")
    expect(tabs.get_by_role("tab", name="Chat")).to_have_attribute("aria-selected", "false")
    card = page.get_by_test_id("overview-changes")
    expect(card.get_by_test_id("overview-snapshot")).to_contain_text(words)
    expect(card.get_by_test_id("overview-snapshot")).to_contain_text(
        f"main {snap['mergeBaseSha'][:7]}…{snap['headSha'][:7]}")

    card.get_by_test_id("overview-review-changes").click()
    expect(tabs.get_by_role("tab", name="Changes")).to_have_attribute("aria-selected", "true")
    changes = page.get_by_test_id("changes-tab")
    expect(changes).to_have_attribute("data-snapshot", snap["id"])
    bar = changes.get_by_test_id("changes-bar")
    expect(bar.get_by_test_id("snapshot-line")).to_contain_text(f"main {snap['mergeBaseSha'][:7]}…{snap['headSha'][:7]}")
    listed = changes.get_by_test_id("branch-diff").get_by_test_id("diff-file")
    expect(listed).to_have_count(count)
    for f in snap["files"]:
        expect(listed.filter(has_text=f["path"]).first).to_be_visible()

    # General feedback, from the bar: a private draft until the batch is submitted.
    text = "Say goodbye as well as hello, in the same tone."
    bar.get_by_test_id("changes-general").click()
    editor = changes.get_by_test_id("changes-general-editor")
    editor.get_by_role("textbox", name="Comment").fill(text)
    editor.get_by_test_id("fb-save").click()
    expect(changes.get_by_test_id("changes-general-editor")).to_have_count(0)
    tray = page.get_by_test_id("fb-tray")
    expect(tray).to_have_attribute("data-count", "1")
    assert [(i["status"], i["anchor"]["kind"], i["body"]) for i in _feedback(client, task["id"])] == [("draft", "general", text)]

    tray.get_by_role("button", name="Review").click()
    expect(tray.get_by_test_id("fb-tray-row")).to_have_count(1)
    tray.get_by_test_id("fb-submit").click()
    dialog = page.get_by_role("dialog", name="Submit 1 feedback item")
    dialog.get_by_test_id("fb-confirm").click()
    expect(page.get_by_test_id("fb-tray")).to_have_count(0)
    submitted = wait_until(lambda: [i for i in _feedback(client, task["id"]) if i["status"] != "draft"], timeout=15,
                           message="nothing submitted")
    assert len(submitted) == 1 and len(_feedback(client, task["id"])) == 1
    [item] = submitted
    assert item["anchor"]["kind"] == "general" and item["body"] == text and item["taskId"] == task["id"], item
    assert console_errors == [], console_errors


def test_a_new_snapshot_is_announced_and_shown_only_on_refresh(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github: FakeGitHub, console_errors: list
):
    task, pr = _delivered(client, forge_project, fake_github, "Pin the snapshot")
    branch = pr["headBranch"]
    snap = _captured(client, task["id"], fake_github.branch_sha(branch), "the branch was never captured")["snapshot"]
    count = snap["stats"]["files"]

    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}/changes")
    changes = page.get_by_test_id("changes-tab")
    expect(changes).to_have_attribute("data-snapshot", snap["id"])
    listed = changes.get_by_test_id("branch-diff").get_by_test_id("diff-file")
    expect(listed).to_have_count(count)
    expect(changes.get_by_test_id("changes-newer")).to_have_count(0)

    # One more commit on the branch, a file of its own; GitHub says the branch moved.
    moved = fake_github.commit_file(branch, "FAREWELL.md", "Goodbye.\n", "Say goodbye")
    later = _captured(client, task["id"], moved, "the new commit was never captured")["snapshot"]
    assert later["relation"] == "fast-forward", later

    newer = changes.get_by_test_id("changes-newer")
    expect(newer).to_contain_text("New changes available", timeout=30_000)
    expect(newer.get_by_test_id("changes-newer-words")).to_have_text(
        f"New commits on the branch — head {snap['headSha'][:7]} → {moved[:7]}")
    # Pinned: the diff on screen is still the first snapshot's.
    expect(changes).to_have_attribute("data-snapshot", snap["id"])
    expect(changes.get_by_test_id("snapshot-head")).to_have_text(snap["headSha"][:7])
    expect(listed).to_have_count(count)
    expect(listed.filter(has_text="FAREWELL.md")).to_have_count(0)

    newer.get_by_test_id("changes-refresh").click()
    expect(changes).to_have_attribute("data-snapshot", later["id"])
    expect(changes.get_by_test_id("snapshot-head")).to_have_text(moved[:7])
    expect(listed).to_have_count(count + 1)
    expect(listed.filter(has_text="FAREWELL.md")).to_have_count(1)
    expect(changes.get_by_test_id("changes-newer")).to_have_count(0)
    assert console_errors == [], console_errors
