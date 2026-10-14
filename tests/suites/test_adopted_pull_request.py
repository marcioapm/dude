"""Work that already exists, taken over: an open pull request, or a branch.

Mirrors test_pull_request_loop.py. The pull request or branch is a person's,
on the local stand-in GitHub (`fake_github.py`); the task names it as its
source before it is delivered, and dude reviews it, fixes what review found
on that same branch by fast-forward, and then either waits on the pull
request it took over (never opening another) or opens one into the base the
person chose.
"""

from __future__ import annotations

from fake_github import FakeGitHub
from helpers import ApiClient, wait_until


def _pull_requests(client: ApiClient, task_id: str) -> list[dict]:
    return client.get("/v1/pull-requests", params={"taskId": task_id}).json()["pullRequests"]


def _task(client: ApiClient, task_id: str) -> dict:
    return client.get(f"/v1/tasks/{task_id}").json()


def _repo_id(forge_project: dict) -> str:
    return forge_project["repositories"][0]["id"]


def test_an_adopted_pull_request_is_reviewed_and_fixed_in_place(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """Review → fix → review → simplify on the person's branch, no implement, and no second pull request."""
    person_head = fake_github.open_pull("feature/greet", title="Greet in Portuguese", author="gus")
    task = client.create_task(
        forge_project["id"], "Finish the Portuguese greeting",
        goal="Take over the open pull request and get it ready to merge.",
        source={"kind": "pr", "repositoryId": _repo_id(forge_project), "pullRequest": "#1"},
    )
    assert task["source"] == {"kind": "pr", "repositoryId": _repo_id(forge_project), "repository": "greeter",
                              "number": 1, "title": "Greet in Portuguese", "headBranch": "feature/greet", "baseBranch": "main"}
    resp = client.post(f"/v1/tasks/{task['id']}/deliver")
    assert resp.status_code == 201, resp.text

    prs = wait_until(lambda: _pull_requests(client, task["id"]), timeout=60, message="the adopted PR was never recorded")
    assert prs[0]["number"] == 1 and prs[0]["headBranch"] == "feature/greet"
    wait_until(
        lambda: _task(client, task["id"])["status"] == "review"
        and any(r["phase"] == "simplify" and r["status"] == "completed" for r in client.task_runs(task["id"])),
        timeout=90, message="the adopted pull request never got back to review",
    )
    phases = [r["phase"] for r in client.task_runs(task["id"]) if r["phase"]]
    assert "implement" not in phases, phases
    assert phases[0] == "review", phases

    # The loop's commits are on the person's branch, on top of theirs.
    log = fake_github.branch_log("feature/greet")
    assert log[0].startswith("Simplify") and "work on feature/greet" in log, log
    assert fake_github._git_out("merge-base", "--is-ancestor", person_head, "feature/greet") == ""
    assert len(fake_github.pulls) == 1, "dude opened a second pull request"

    # Feedback on it is fixed on the same branch.
    head = fake_github.branch_sha("feature/greet")
    fixes = len([r for r in client.task_runs(task["id"]) if r["phase"] == "fix"])
    fake_github.comment(1, "Please also note the date in FIXED.md.", path="FIXED.md")
    wait_until(lambda: fake_github.branch_sha("feature/greet") != head, timeout=60,
               message="the PR fix never reached the adopted branch")
    assert len([r for r in client.task_runs(task["id"]) if r["phase"] == "fix"]) == fixes + 1
    assert len(fake_github.pulls) == 1, "dude opened a second pull request"
    assert len(_pull_requests(client, task["id"])) == 1


def test_an_adopted_branch_opens_its_pull_request_into_the_chosen_base(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """A stacked branch: reviewed, fixed on itself, then a pull request into its base, not main."""
    fake_github.push_branch("stack/base")
    fake_github.push_branch("stack/top", base="stack/base")
    task = client.create_task(
        forge_project["id"], "Finish the stacked change",
        goal="Take over the stacked branch and open its pull request.",
        source={"kind": "branch", "repositoryId": _repo_id(forge_project), "branch": "stack/top", "base": "stack/base"},
    )
    assert task["source"]["headBranch"] == "stack/top" and task["source"]["baseBranch"] == "stack/base"
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    pr = wait_until(lambda: _pull_requests(client, task["id"]), timeout=90,
                    message="the adopted branch never got a pull request")[0]
    assert pr["headBranch"] == "stack/top"
    assert pr["baseBranch"] == "stack/base"
    forge_pr = fake_github.pulls[pr["number"]]
    assert (forge_pr.head, forge_pr.base) == ("stack/top", "stack/base")
    phases = [r["phase"] for r in client.task_runs(task["id"]) if r["phase"]]
    assert "implement" not in phases and phases[0] == "review", phases
    assert "work on stack/top" in fake_github.branch_log("stack/top")


def test_a_source_dude_must_not_touch_is_refused_with_why(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """Each refusal is the API's answer, with its code, and no task is made."""
    repo = _repo_id(forge_project)
    fake_github.open_pull("feature/merged")
    fake_github.merge(1)
    fake_github.open_pull("feature/fork", head_repo="mallory/greeter")
    fake_github.push_branch("release/2.0")
    fake_github.push_branch("feature/locked")
    fake_github.protected.add("feature/locked")
    cases = [
        ({"kind": "pr", "repositoryId": repo, "pullRequest": "1"}, 422, "pr_merged"),
        ({"kind": "pr", "repositoryId": repo, "pullRequest": "2"}, 422, "pr_fork"),
        ({"kind": "pr", "repositoryId": repo, "pullRequest": "99"}, 422, "pr_not_found"),
        ({"kind": "branch", "repositoryId": repo, "branch": "main", "base": "release/2.0"}, 422, "default_branch"),
        ({"kind": "branch", "repositoryId": repo, "branch": "release/2.0"}, 422, "release_branch"),
        ({"kind": "branch", "repositoryId": repo, "branch": "feature/locked"}, 422, "protected"),
        ({"kind": "branch", "repositoryId": "repo_not_here", "branch": "x"}, 422, "not_in_project"),
    ]
    before = len(client.get("/v1/tasks", params={"projectId": forge_project["id"]}).json()["tasks"])
    for source, status, code in cases:
        resp = client.post("/v1/tasks", {"projectId": forge_project["id"], "title": "Adopt",
                                         "goal": "Take over existing work for the refusal tests.", "source": source})
        assert resp.status_code == status, (source, resp.text)
        assert resp.json()["error"]["code"] == code, (source, resp.text)
    assert len(client.get("/v1/tasks", params={"projectId": forge_project["id"]}).json()["tasks"]) == before

    # Another live task's branch is taken.
    fake_github.push_branch("feature/shared")
    client.create_task(forge_project["id"], "First", goal="Take over the shared branch first.",
                       source={"kind": "branch", "repositoryId": repo, "branch": "feature/shared"})
    resp = client.post("/v1/tasks", {"projectId": forge_project["id"], "title": "Second", "goal": "Take over the shared branch too.",
                                     "source": {"kind": "branch", "repositoryId": repo, "branch": "feature/shared"}})
    assert resp.status_code == 422 and resp.json()["error"]["code"] == "branch_taken", resp.text

    # A lookup the dialog makes says the same, and writes nothing.
    resp = client.post(f"/v1/projects/{forge_project['id']}/sources/check",
                       {"source": {"kind": "pr", "repositoryId": repo, "pullRequest": "2"}})
    assert resp.status_code == 422 and resp.json()["error"]["code"] == "pr_fork", resp.text


def test_a_started_tasks_source_is_fixed(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    fake_github.open_pull("feature/one")
    task = client.create_task(forge_project["id"], "Adopt one", goal="Take over the first pull request.",
                              source={"kind": "pr", "repositoryId": _repo_id(forge_project), "pullRequest": "1"})
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    resp = client.patch(f"/v1/tasks/{task['id']}", {"source": None})
    assert resp.status_code == 409, resp.text
