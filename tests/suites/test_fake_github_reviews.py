"""The Python fake GitHub's review writes, driven over HTTP as dude's forge
drives them, answering as the Go fake (fakegithub/reviews.go) does.

Pure checks; they need git, and no environment, browser or services:

    uv run --directory tests pytest suites/test_fake_github_reviews.py
"""

import contextlib
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest
import requests

from fake_github import FakeGitHub
from helpers import wait_until

HEADERS = {"authorization": "Bearer fake-token"}


@pytest.fixture
def hooks():
    """A webhook receiver: each delivery's event and payload, in order."""
    class Got(list):
        url = ""

    got = Got()

    class Hook(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            body = self.rfile.read(int(self.headers["content-length"]))
            got.append((self.headers["x-github-event"], json.loads(body)))
            self.send_response(204)
            self.end_headers()

    server = ThreadingHTTPServer(("127.0.0.1", 0), Hook)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    got.url = f"http://127.0.0.1:{server.server_port}/hook"
    yield got
    server.shutdown()
    server.server_close()


@pytest.fixture
def gh(tmp_path, hooks):
    fake = FakeGitHub(tmp_path, owner="acme", repo="target")
    fake.start()
    fake.webhook_url, fake.webhook_secret = hooks.url, "s"
    yield fake
    fake.stop()


BASE = "".join(f"line {i}\n" for i in range(1, 21))  # greet.py on main: 20 lines


@pytest.fixture
def pr(gh):
    """A person's pull request (gus's) replacing greet.py's line 2 with two
    lines: one hunk, lines 1-5 on the base (LEFT, with line 2 removed) and
    1-6 on the head (RIGHT). Its number and head."""
    gh.write_file("main", "greet.py", BASE, "add greet")
    gh.open_pull("feature", author="gus")
    return 1, gh.write_file("feature", "greet.py", BASE.replace("line 2\n", "two\ntwo more\n"), "greet")


def post(gh, number, body):
    return requests.post(f"{gh.api_url}/repos/acme/target/pulls/{number}/reviews", json=body, headers=HEADERS, timeout=10)


def dismiss(gh, number, review):
    return requests.put(f"{gh.api_url}/repos/acme/target/pulls/{number}/reviews/{review}/dismissals",
                        json={"message": "stale", "event": "DISMISS"}, headers=HEADERS, timeout=10)


def test_a_review_is_pinned_to_its_commit_with_its_line_comments(gh, pr, hooks):
    number, head = pr
    gh.write_file("feature", "greet.py", "later\n", "later")  # the branch moves on; the review stays put
    gh.pulls[number].requested_reviewers = ["dude-bot", "ana"]
    resp = post(gh, number, {"commit_id": head, "event": "REQUEST_CHANGES", "body": "see",
                             "comments": [{"path": "greet.py", "line": 3, "start_line": 1, "body": "why"}]})
    assert resp.status_code == 200, resp.text
    review = resp.json()
    assert review["state"] == "CHANGES_REQUESTED" and review["commit_id"] == head and review["body"] == "see"
    assert review["user"]["login"] == "dude-bot" and review["node_id"] == f"PRR_{review['id']}"
    assert review["html_url"].endswith(f"/acme/target/pull/{number}#pullrequestreview-{review['id']}")
    assert review["submitted_at"]
    listed = requests.get(f"{gh.api_url}/repos/acme/target/pulls/{number}/reviews", headers=HEADERS, timeout=10).json()
    assert [r["id"] for r in listed] == [review["id"]]
    assert gh.pulls[number].requested_reviewers == ["ana"]
    comments = requests.get(f"{gh.api_url}/repos/acme/target/pulls/{number}/comments", headers=HEADERS, timeout=10).json()
    assert [(c["path"], c["line"], c["body"], c["pull_request_review_id"]) for c in comments] == \
        [("greet.py", 3, "why", review["id"])]
    # The fake answers first and tells by webhook after, as GitHub does.
    submitted = ("pull_request_review", {"action": "submitted", "pull_request": {"number": number},
                                         "repository": {"full_name": "acme/target"}})
    # wait_until raises on a timeout; let the assert below fail instead, so
    # pytest prints the deliveries as they were at the end.
    with contextlib.suppress(AssertionError):
        wait_until(lambda: submitted in hooks, timeout=10, interval=0.05)
    assert submitted in hooks


def test_a_review_without_commit_id_is_refused(gh, pr):
    resp = post(gh, pr[0], {"event": "COMMENT", "body": "x"})
    assert resp.status_code == 422
    assert resp.json() == {"message": "Unprocessable Entity", "errors": ["commit_id is required"]}
    assert gh.pulls[pr[0]].reviews == []


def test_an_unknown_commit_and_an_unknown_event_are_refused(gh, pr):
    resp = post(gh, pr[0], {"event": "COMMENT", "commit_id": "f" * 40})
    assert (resp.status_code, resp.json()["errors"]) == (422, [f"No commit found for SHA: {'f' * 40}"])
    resp = post(gh, pr[0], {"event": "PENDING", "commit_id": pr[1]})
    assert (resp.status_code, resp.json()["errors"]) == (422, ['Event "PENDING" is not one of APPROVE, REQUEST_CHANGES, COMMENT'])


@pytest.mark.parametrize(("comment", "reason"), [
    ({"path": "greet.py", "line": 9}, "Pull request review thread line must be part of the diff"),
    ({"path": "README.md", "line": 1}, "Pull request review thread line must be part of the diff"),
    ({"path": "greet.py", "line": 12, "side": "LEFT"}, "Pull request review thread line must be part of the diff"),
    # LEFT 6 is past the hunk on the base (1-5), though RIGHT 6 is in it.
    ({"path": "greet.py", "line": 6, "side": "LEFT"}, "Pull request review thread line must be part of the diff"),
    ({"path": "greet.py", "line": 2, "start_line": 7},
     "Pull request review thread start line must be part of the same hunk as the line."),
    # LEFT 6 is past the hunk on the base, though RIGHT 6 is in it.
    ({"path": "greet.py", "line": 3, "start_line": 6, "start_side": "LEFT"},
     "Pull request review thread start line must be part of the same hunk as the line."),
])
def test_a_comment_off_the_diff_is_refused_with_the_reason_in_errors(gh, pr, comment, reason):
    resp = post(gh, pr[0], {"event": "COMMENT", "commit_id": pr[1], "comments": [{**comment, "body": "x"}]})
    assert resp.status_code == 422
    assert resp.json() == {"message": "Unprocessable Entity", "errors": [reason]}
    assert gh.pulls[pr[0]].reviews == [] and gh.pulls[pr[0]].comments == []


def test_the_base_side_takes_comments_on_its_own_lines(gh, pr):
    """A removed line is commented on LEFT, and a range may start on the
    base and end on the head within one hunk."""
    comments = [{"path": "greet.py", "line": 2, "side": "LEFT", "body": "removed"},
                {"path": "greet.py", "line": 3, "side": "RIGHT", "start_line": 2, "start_side": "LEFT", "body": "both"}]
    resp = post(gh, pr[0], {"event": "COMMENT", "commit_id": pr[1], "comments": comments})
    assert resp.status_code == 200, resp.text
    assert [(c["line"], c["body"]) for c in gh.pulls[pr[0]].comments] == [(2, "removed"), (3, "both")]


def test_the_author_may_not_approve_or_request_changes_on_its_own_pull_request(gh, pr):
    gh.pulls[pr[0]].author = "dude-bot"
    resp = post(gh, pr[0], {"event": "APPROVE", "commit_id": pr[1]})
    assert resp.status_code == 422 and resp.json()["errors"] == ["Can not approve your own pull request"]
    resp = post(gh, pr[0], {"event": "REQUEST_CHANGES", "commit_id": pr[1]})
    assert resp.status_code == 422 and resp.json()["errors"] == ["Can not request changes on your own pull request"]
    assert post(gh, pr[0], {"event": "COMMENT", "commit_id": pr[1]}).json()["state"] == "COMMENTED"


def test_a_dismissal_dismisses_once(gh, pr):
    review = post(gh, pr[0], {"event": "APPROVE", "commit_id": pr[1]}).json()
    resp = dismiss(gh, pr[0], review["id"])
    assert resp.status_code == 200 and resp.json()["state"] == "DISMISSED"
    assert gh.pulls[pr[0]].reviews[0]["state"] == "DISMISSED"
    again = dismiss(gh, pr[0], review["id"])
    assert again.status_code == 422
    assert again.json() == {"message": "Unprocessable Entity",
                            "errors": ["Can not dismiss a review that has already been dismissed"]}
    assert dismiss(gh, pr[0], 1).status_code == 404


def test_a_commented_review_is_not_dismissable(gh, pr):
    review = post(gh, pr[0], {"event": "COMMENT", "commit_id": pr[1]}).json()
    resp = dismiss(gh, pr[0], review["id"])
    assert resp.status_code == 422 and resp.json()["errors"] == ["Can not dismiss a commented pull request review"]


def test_refuse_dismissal_answers_403_as_branch_protection(gh, pr):
    review = post(gh, pr[0], {"event": "REQUEST_CHANGES", "commit_id": pr[1]}).json()
    gh.refuse_dismissal = True
    resp = dismiss(gh, pr[0], review["id"])
    assert resp.status_code == 403
    assert resp.json() == {"message": "You are not authorized to dismiss this pull request review due to protected branch rules."}
    assert gh.pulls[pr[0]].reviews[0]["state"] == "CHANGES_REQUESTED"


def test_drop_review_response_once_takes_the_review_and_drops_the_answer(gh, pr):
    gh.drop_review_response_once = True
    with pytest.raises(requests.ConnectionError):
        post(gh, pr[0], {"event": "COMMENT", "commit_id": pr[1], "body": "first"})
    assert [r["body"] for r in gh.pulls[pr[0]].reviews] == ["first"]
    assert post(gh, pr[0], {"event": "COMMENT", "commit_id": pr[1], "body": "second"}).status_code == 200


def test_review_fail_5xx_fails_that_many_posts_and_takes_none(gh, pr):
    gh.review_fail_5xx = 2
    assert [post(gh, pr[0], {"event": "COMMENT", "commit_id": pr[1]}).status_code for _ in range(3)] == [502, 502, 200]
    assert len(gh.pulls[pr[0]].reviews) == 1


def test_request_review_sends_review_requested(gh, pr, hooks):
    gh.request_review(pr[0], "ana")
    assert gh.pulls[pr[0]].requested_reviewers == ["ana"]
    assert hooks[-1] == ("pull_request", {"action": "review_requested", "pull_request": {"number": pr[0]},
                                          "requested_reviewer": {"login": "ana"},
                                          "repository": {"full_name": "acme/target"}})
