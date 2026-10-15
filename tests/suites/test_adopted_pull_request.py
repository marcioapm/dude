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
                              "number": 1, "title": "Greet in Portuguese", "headBranch": "feature/greet", "baseBranch": "main",
                              "mode": "review"}
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
    """A stacked branch to review as it is: reviewed, fixed on itself, then a pull request into its base, not main."""
    fake_github.push_branch("stack/base")
    fake_github.push_branch("stack/top", base="stack/base")
    task = client.create_task(
        forge_project["id"], "Finish the stacked change",
        goal="Take over the stacked branch and open its pull request.",
        source={"kind": "branch", "repositoryId": _repo_id(forge_project), "branch": "stack/top", "base": "stack/base",
                "mode": "review"},
    )
    assert task["source"]["headBranch"] == "stack/top" and task["source"]["baseBranch"] == "stack/base"
    assert task["source"]["mode"] == "review"
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    # A person's branch is never published without their Open or Draft, even under Deliver.
    wait_until(lambda: _task(client, task["id"])["status"] == "awaiting_input", timeout=90,
               message="the adopted branch never waited for Open or Draft")
    assert _pull_requests(client, task["id"]) == [] and len(fake_github.pulls) == 0
    resp = client.post(f"/v1/tasks/{task['id']}/decider", {"decider": "policy"})
    assert resp.status_code == 409 and resp.json()["error"]["code"] == "pull_request_gate", resp.text
    resp = client.post(f"/v1/tasks/{task['id']}/decider", {"decider": "policy", "openPullRequest": True, "draft": True})
    assert resp.status_code == 200, resp.text

    pr = wait_until(lambda: _pull_requests(client, task["id"]), timeout=90,
                    message="the adopted branch never got a pull request")[0]
    assert pr["headBranch"] == "stack/top"
    assert pr["baseBranch"] == "stack/base"
    forge_pr = fake_github.pulls[pr["number"]]
    assert (forge_pr.head, forge_pr.base, forge_pr.draft) == ("stack/top", "stack/base", True)
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
        ({"kind": "pr", "repositoryId": repo, "pullRequest": f"{fake_github.api_url}/{fake_github.owner}/other/pull/1"},
         422, "bad_pull_request"),
        ({"kind": "pr", "repositoryId": repo, "pullRequest": f"https://example.com/{fake_github.owner}/{fake_github.repo}/pull/1"},
         422, "bad_pull_request"),
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


def test_the_branch_pickers_list_the_repositorys_branches(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """The dialog's branch and base pickers: what was typed, matched against GitHub's branch list."""
    fake_github.push_branch("stack/base")
    fake_github.push_branch("stack/top", base="stack/base")
    fake_github.push_branch("feature/greet")
    path = f"/v1/projects/{forge_project['id']}/sources/repositories/{_repo_id(forge_project)}/branches"
    resp = client.get(path, params={"q": "stack"})
    assert resp.status_code == 200, resp.text
    assert sorted(resp.json()["branches"]) == ["stack/base", "stack/top"] and resp.json()["more"] is False, resp.text
    resp = client.get(path, params={"q": ""})
    assert resp.status_code == 200, resp.text
    assert {"main", "stack/base", "stack/top", "feature/greet"} <= set(resp.json()["branches"]), resp.text


def test_a_started_tasks_source_is_fixed(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    fake_github.open_pull("feature/one")
    task = client.create_task(forge_project["id"], "Adopt one", goal="Take over the first pull request.",
                              source={"kind": "pr", "repositoryId": _repo_id(forge_project), "pullRequest": "1"})
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    resp = client.patch(f"/v1/tasks/{task['id']}", {"source": None})
    assert resp.status_code == 409, resp.text


def _phases(client: ApiClient, task_id: str) -> list[str]:
    return [r["phase"] for r in client.task_runs(task_id) if r["phase"]]


def _simplified(client: ApiClient, task_id: str) -> bool:
    return any(r["phase"] == "simplify" and r["status"] == "completed" for r in client.task_runs(task_id))


def test_a_draft_pull_request_is_continued_on_its_own_branch_then_reviewed(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """(a) A draft is unfinished: by default an implementer builds on it, pushing onto the same head, then review; one PR."""
    person_head = fake_github.open_pull("feature/half", title="Half a greeting", draft=True)
    task = client.create_task(
        forge_project["id"], "Finish the half greeting",
        goal="Carry the draft greeting on to done.",
        source={"kind": "pr", "repositoryId": _repo_id(forge_project), "pullRequest": "#1"},
    )
    assert task["source"]["mode"] == "continue"
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    wait_until(lambda: _task(client, task["id"])["status"] == "review" and _simplified(client, task["id"]),
               timeout=120, message="the continued draft never got back to review")
    phases = _phases(client, task["id"])
    assert phases[:2] == ["implement", "review"], phases
    log = fake_github.branch_log("feature/half")
    assert any(m.startswith("Add FACTORY.md") for m in log) and "work on feature/half" in log, log
    assert fake_github._git_out("merge-base", "--is-ancestor", person_head, "feature/half") == ""
    assert len(fake_github.pulls) == 1, "dude opened a second pull request"
    assert [pr["number"] for pr in _pull_requests(client, task["id"])] == [1]


def test_a_ready_pull_request_to_continue_with_nothing_to_add_is_reviewed_as_it_is(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """(b) Ready, but Continue chosen; the implementer commits nothing: review on the unchanged head, said in the ledger."""
    resp = client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models({"implementer": "fake/done"})})
    assert resp.status_code == 200, resp.text
    person_head = fake_github.open_pull("feature/done", title="Done already")
    task = client.create_task(
        forge_project["id"], "Check the finished greeting",
        goal="Carry the greeting on to done, if it is not.",
        source={"kind": "pr", "repositoryId": _repo_id(forge_project), "pullRequest": "#1", "mode": "continue"},
    )
    assert task["source"]["mode"] == "continue"
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    review = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "review"), None),
                        timeout=90, message="no review after an implementer with nothing to add")
    implement = [r for r in client.task_runs(task["id"]) if r["phase"] == "implement"]
    assert len(implement) == 1 and implement[0]["status"] == "completed", implement
    assert client.get_run(review["id"])["baseRefs"] == {"greeter": person_head}
    events = client.events(taskId=task["id"])
    said = [e for e in events if e["eventType"] == "run.nothing_to_add"]
    assert [e["runId"] for e in said] == [implement[0]["id"]], said
    assert not any(e["eventType"] == "task.status_changed" and e["payload"].get("reason") == "no_changes" for e in events)
    wait_until(lambda: _simplified(client, task["id"]), timeout=120, message="the review loop never finished")
    assert "work on feature/done" in fake_github.branch_log("feature/done")
    assert not any(m.startswith("Add FACTORY.md") for m in fake_github.branch_log("feature/done"))
    assert len(fake_github.pulls) == 1


def test_a_bare_branch_is_continued_then_held_for_open_or_draft(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """(c) A branch continues by default: implement → review → the Open/Draft gate, as a branch to review is held."""
    person_head = fake_github.push_branch("feature/bare")
    task = client.create_task(
        forge_project["id"], "Finish the bare branch",
        goal="Carry the bare branch on and open its pull request.",
        source={"kind": "branch", "repositoryId": _repo_id(forge_project), "branch": "feature/bare"},
    )
    assert task["source"]["mode"] == "continue"
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    wait_until(lambda: _task(client, task["id"])["status"] == "awaiting_input", timeout=120,
               message="the continued branch never waited for Open or Draft")
    assert _pull_requests(client, task["id"]) == [] and len(fake_github.pulls) == 0
    phases = _phases(client, task["id"])
    assert phases[:2] == ["implement", "review"], phases
    assert fake_github._git_out("merge-base", "--is-ancestor", person_head, "feature/bare") == ""
    resp = client.post(f"/v1/tasks/{task['id']}/decider", {"decider": "policy", "openPullRequest": True, "draft": False})
    assert resp.status_code == 200, resp.text
    pr = wait_until(lambda: _pull_requests(client, task["id"]), timeout=90, message="no pull request after Open")[0]
    assert (pr["headBranch"], pr["baseBranch"]) == ("feature/bare", "main")


def test_a_ready_pull_request_by_default_is_reviewed_with_no_implementer(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """(d) Ready for review and nothing chosen: review as it is, as before (no implementer)."""
    fake_github.open_pull("feature/ready", title="Ready greeting")
    task = client.create_task(
        forge_project["id"], "Review the ready greeting", goal="Get the ready greeting merged.",
        source={"kind": "pr", "repositoryId": _repo_id(forge_project), "pullRequest": "#1"},
    )
    assert task["source"]["mode"] == "review"
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    wait_until(lambda: _simplified(client, task["id"]), timeout=120, message="the review loop never finished")
    phases = _phases(client, task["id"])
    assert "implement" not in phases and phases[0] == "review", phases
