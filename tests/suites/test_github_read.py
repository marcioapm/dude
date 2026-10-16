"""A brainstorm reads GitHub, end to end through the deployed processes.

The session's agent (the scripted one) is linked to a project whose
repository is on the local stand-in GitHub (`fake_github.py`). Told to by
a message's "tool:" lines, it searches that repository's pull requests
with `github_search`, reads the one it found with `github_pull_request`
(its number taken from the search's answer), and says what it read: the
pull request's title reaches the session's visible answer, quoted as
external text, while no event keeps what GitHub said.
"""

from __future__ import annotations

import json
import os

from fake_github import FakeGitHub
from helpers import ApiClient, wait_until


def _said(client: ApiClient, session: str) -> list[str]:
    return [e["payload"]["text"] for e in client.events(sessionId=session, limit=1000) if e["eventType"] == "agent.message"]


def test_a_brainstorm_finds_a_pull_request_on_github_and_answers_with_its_title(client: ApiClient, fake_github: FakeGitHub):
    resp = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token", "apiBaseUrl": fake_github.api_url})
    assert resp.status_code == 200, resp.text
    tier = client.tier_for("fake/scripted")
    assert client.patch("/v1/settings/organization", {"roles": {"brainstorm": {"tier": tier}}}).status_code == 200
    project = client.create_project(name="Greeter", slug=f"gr-{int.from_bytes(os.urandom(3)):08d}",
                                    repositories=[{"name": "greeter", "url": fake_github.clone_url, "defaultBranch": "main"}])
    slug = f"{fake_github.owner}/{fake_github.repo}"

    # A person's pull request on GitHub, with a file it changes and a review's words.
    fake_github.open_pull("feature/ola", title="Greet in Portuguese", author="gus")
    fake_github.write_file("feature/ola", "greet.py", "print('olá')\n", "say olá")
    secret_words = "Only the reviewer named Quillon wrote this sentence"
    fake_github.review(1, "COMMENTED", body=secret_words, reviewer="quillon")
    fake_github.comment(1, "Keep the accent on olá.", author="ana", path="greet.py")
    fake_github.open_issue("Rename the greeting module", "Unrelated to Portuguese.")

    session = client.post("/v1/brainstorms", {"title": "Greetings", "projects": [
        {"projectId": project["id"], "repositoryIds": [project["repositories"][0]["id"]]}]}).json()["id"]
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "what is open on GitHub?"})
    wait_until(lambda: _said(client, session), timeout=60, message="the session's agent never answered")

    said = len(_said(client, session))
    search = json.dumps({"repo": slug, "query": "Portuguese", "kind": "pr", "state": "open"})
    lines = [
        "which pull request is the Portuguese one?",
        f"tool: github_search {search}",
        f'tool: github_pull_request {{"repo": "{slug}", "number": {{{{last.items.0.number}}}}, "files": ["greet.py"]}}',
        'tool: say {"text": "Found: {{last.pullRequest.title}} changing {{last.files.0.path}}"}',
        'tool: say {"text": "Open thread by {{last.unresolvedThreads.0.comments.0.author}} on {{last.unresolvedThreads.0.path}}"}',
    ]
    # The agent is running, so the message is its next input (200), not a new Run (201).
    resp = client.post(f"/v1/brainstorms/{session}/chat", {"text": "\n".join(lines)})
    assert resp.status_code == 200, resp.text
    answer = wait_until(lambda: "\n".join(_said(client, session)[said:]) if any(
        "Open thread by" in t for t in _said(client, session)[said:]) else None, timeout=60, message="the agent never said what it read")

    assert "dude github_" not in answer, answer  # no tool failed
    found = next(line for line in answer.splitlines() if line.startswith("Found:"))
    title = found.removeprefix("Found: ").split(" changing ")[0]
    assert title.startswith("⟦external ") and "Greet in Portuguese" in title and title.endswith("⟧"), found
    assert "greet.py" in found, found
    thread = next(line for line in answer.splitlines() if line.startswith("Open thread by "))
    assert thread.startswith("Open thread by ana on ⟦external ") and "greet.py" in thread, thread

    events = client.events(sessionId=session, limit=1000)
    reads = [e["payload"] for e in events if e["eventType"] == "github.read"]
    assert [(r["tool"], r["repo"]) for r in reads] == [("github_search", slug), ("github_pull_request", slug)], reads
    assert reads[1]["number"] == 1 and reads[1]["files"] == ["greet.py"], reads
    # What GitHub said reached the agent alone: no event holds the description.
    assert secret_words not in json.dumps(events)
