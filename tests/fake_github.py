"""A stand-in for GitHub's REST API, and a git daemon to push to.

The PR loop's end-to-end test needs two things GitHub provides: somewhere to
push a branch, and an API that opens a pull request and reports its state.
Using real GitHub would make the suite depend on the network, a token and a
repository nobody else touches. This provides both locally:

  - `git daemon` serves a bare repository over `git://`, so the runner's push
    is a real push and the repository URL parses to a real `owner/repo`;
  - a small HTTP server implements the handful of endpoints the factory calls,
    reading branches straight out of that bare repository.

Only what the factory uses is implemented. A test drives the "human" side —
leaving a comment, reviewing, pushing, reporting CI, merging — through
`FakeGitHub` directly, and each tells dude by a signed webhook as GitHub
would.
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import json
import re
import uuid
import subprocess
import threading
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


from env import find_free_port as _free_port  # noqa: E402


def _now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


# Who the fake says can review: the reviewer picker's people.
REVIEWERS = [
    {"login": "ana", "name": "Ana Ribeiro", "avatarUrl": ""},
    {"login": "tom", "name": "Tom Okafor", "avatarUrl": ""},
    {"login": "bo", "name": "Bo Lindqvist", "avatarUrl": ""},
]

@dataclass
class TokenProfile:
    """What GitHub lets one token do, as GitHub answers it.

    The default can do everything and is neither classic nor fine-grained by
    its headers. `scopes` makes it classic: every answer carries them in
    X-OAuth-Scopes. `checks=False` is a fine-grained token, which GitHub
    offers no Checks permission: 403 on the check runs of a commit that has
    some, and 200 with none on a commit that has none, as GitHub answers.
    """

    scopes: str | None = None
    checks: bool = True
    pulls_write: bool = True
    push: bool = True
    hooks_write: bool = True
    # Endpoints answered with a rate-limit 403 ("check-runs").
    rate_limited: set[str] = field(default_factory=set)


@dataclass
class PullRequest:
    number: int
    head: str
    base: str
    title: str
    body: str
    draft: bool
    state: str = "open"
    merged_at: str | None = None
    comments: list[dict] = field(default_factory=list)
    reviews: list[dict] = field(default_factory=list)
    requested_reviewers: list[str] = field(default_factory=list)
    # GitHub says the branch conflicts with its base.
    conflicting: bool = False
    # Review threads nobody has resolved (only GraphQL says).
    unresolved_threads: int = 0


class FakeGitHub:
    """A bare repository, a git daemon serving it, and an API in front of both."""

    def __init__(self, root: Path, owner: str = "acme", repo: str = "target", listen: str = "127.0.0.1") -> None:
        # The address git is served on. Loopback for the fake lux; a real
        # lux's hosts are containers, and reach this machine at their
        # network's gateway.
        self.listen = listen
        self.owner = owner
        self.repo = repo
        self.root = root
        self.bare = root / owner / f"{repo}.git"
        self._next_id = 1000
        self._lock = threading.Lock()
        self._daemon: subprocess.Popen | None = None
        self._server: ThreadingHTTPServer | None = None
        self.git_port = _free_port()
        self.api_port = _free_port()
        # Where to deliver webhooks, and the secret to sign them with. Set
        # by the test once dude has stored a credential and minted a secret.
        self.webhook_url: str | None = None
        self.webhook_secret: str | None = None
        # Other repositories of the same owner are served by this one's
        # daemon and API (add_repository).
        self._parent: FakeGitHub | None = None
        # Repository permission by login (admin, write, read, none); anyone
        # not named has write access, as the people in these tests are the
        # team. And the owner organization's members.
        self.permissions: dict[str, str] = {}
        self.members: set[str] = set()
        # The tokens GitHub knows, by value; shared with sibling repositories.
        self.tokens: dict[str, TokenProfile] = {"fake-token": TokenProfile()}
        # Who owns the repositories, as GitHub's repository answer says.
        self.owner_type = "User"
        # Who the token comments as.
        self.token_login = "dude-bot"
        self._init_repository()

    def _init_repository(self) -> None:
        """What each repository has of its own; the rest a sibling shares."""
        self.pulls: dict[int, PullRequest] = {}
        self.hooks: list[dict] = []
        self.deliveries: list[tuple[str, int]] = []
        self.siblings: dict[str, "FakeGitHub"] = {}
        # CI, by commit: each check by name — a check run (GitHub Actions,
        # an app) or a commit status.
        self.checks: dict[str, dict[str, dict]] = {}
        # What dude asked GitHub to do. An Actions check run is a job,
        # re-run through the Actions API.
        self.merges: list[dict] = []
        self.updates: list[int] = []
        self.jobs_rerun: list[int] = []
        self.review_requests: list[tuple[int, list[str]]] = []
        self.receive_requests: list[str] = []

    def add_repository(self, repo: str) -> "FakeGitHub":
        """Another repository, served alongside this one: same owner, same
        git daemon, same API, its own branches and pull requests."""
        sib = FakeGitHub.__new__(FakeGitHub)
        sib.__dict__.update(self.__dict__)
        sib._init_repository()
        sib.repo, sib.bare = repo, self.root / self.owner / f"{repo}.git"
        sib._lock, sib._parent = threading.Lock(), self
        sib._seed(suffix=repo)
        self.siblings[repo] = sib
        return sib

    def _for_path(self, path: str) -> "FakeGitHub":
        """The repository an API path is about: /repos/<owner>/<repo>/…"""
        parts = path.split("/")
        if len(parts) > 3 and parts[1] == "repos" and parts[3] in self.siblings:
            return self.siblings[parts[3]]
        return self

    def _for_repo(self, repo: str) -> "FakeGitHub":
        return self.siblings.get(repo, self)

    # -- lifecycle -----------------------------------------------------------

    def start(self) -> None:
        self._seed()
        self._daemon = subprocess.Popen(
            [
                "git", "daemon",
                f"--base-path={self.root}",
                f"--port={self.git_port}",
                "--export-all",
                # Agents push through lux; git daemon refuses that unless asked.
                "--enable=receive-pack",
                "--reuseaddr",
                f"--listen={self.listen}",
                str(self.root),
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        self._server = ThreadingHTTPServer((self.listen, self.api_port), self._handler())
        threading.Thread(target=self._server.serve_forever, daemon=True).start()
        self._wait_for_daemon()

    def stop(self) -> None:
        if self._server:
            self._server.shutdown()
            # Release the socket, so a late caller is refused at once rather
            # than left waiting on a listener nobody serves.
            self._server.server_close()
        if self._daemon:
            self._daemon.terminate()
            self._daemon.wait(timeout=5)

    @property
    def clone_url(self) -> str:
        return f"git://{self.listen}:{self.git_port}/{self.owner}/{self.repo}.git"

    @property
    def api_url(self) -> str:
        return f"http://{self.listen}:{self.api_port}"

    def _seed(self, suffix: str = "") -> None:
        seed = self.root / f"seed-{self.owner}{suffix}"
        seed.mkdir(parents=True)
        for args in (
            ["git", "init", "-q", "--initial-branch=main"],
            ["git", "config", "user.email", "t@example.com"],
            ["git", "config", "user.name", "Test"],
        ):
            subprocess.run(args, cwd=seed, check=True)
        (seed / "README.md").write_text("fake github fixture\n")
        subprocess.run(["git", "add", "."], cwd=seed, check=True)
        subprocess.run(["git", "commit", "-q", "-m", "initial"], cwd=seed, check=True)
        self.bare.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(["git", "clone", "-q", "--bare", str(seed), str(self.bare)], check=True)

    def _wait_for_daemon(self, timeout: float = 10.0) -> None:
        deadline = time.time() + timeout
        while time.time() < deadline:
            result = subprocess.run(
                ["git", "ls-remote", self.clone_url], capture_output=True, timeout=5
            )
            if result.returncode == 0:
                return
            time.sleep(0.2)
        raise RuntimeError("git daemon did not come up")

    # -- the "human" side ----------------------------------------------------

    def branch_sha(self, branch: str) -> str | None:
        result = subprocess.run(
            ["git", "rev-parse", "--verify", "-q", f"refs/heads/{branch}"],
            cwd=self.bare, capture_output=True, text=True,
        )
        return result.stdout.strip() or None

    def branches(self) -> list[str]:
        result = subprocess.run(
            ["git", "for-each-ref", "--format=%(refname:short)", "refs/heads"],
            cwd=self.bare, capture_output=True, text=True, check=True,
        )
        return result.stdout.split()

    def file_at(self, branch: str, path: str) -> str | None:
        """A file's contents on a branch; None when it is absent."""
        result = subprocess.run(
            ["git", "show", f"refs/heads/{branch}:{path}"],
            cwd=self.bare, capture_output=True, text=True,
        )
        return result.stdout if result.returncode == 0 else None

    def branch_log(self, branch: str) -> list[str]:
        result = subprocess.run(
            ["git", "log", "--format=%s", f"refs/heads/{branch}"],
            cwd=self.bare, capture_output=True, text=True,
        )
        return [line for line in result.stdout.splitlines() if line]

    def comment(self, number: int, body: str, author: str = "reviewer", path: str | None = None) -> int:
        """A person's comment, on the conversation or (with a path) on a line; its id."""
        with self._lock:
            self._next_id += 1
            anchor = "discussion_r" if path else "issuecomment-"
            self.pulls[number].comments.append(
                {"id": self._next_id, "body": body, "user": {"login": author},
                 "created_at": _now(), "path": path,
                 "html_url": f"https://github.test/{self.owner}/{self.repo}/pull/{number}#{anchor}{self._next_id}"}
            )
            comment_id = self._next_id
        if path:
            self.send_webhook("pull_request_review_comment", {"action": "created", "pull_request": {"number": number}})
        else:
            self.send_webhook("issue_comment", {"action": "created", "issue": {"number": number, "pull_request": {"url": ""}}})
        return comment_id

    def approve(self, number: int, reviewer: str = "alice") -> None:
        """A reviewer approves the pull request, and GitHub says so by webhook."""
        self.review(number, "APPROVED", reviewer=reviewer)

    def review(self, number: int, state: str, body: str = "", reviewer: str = "alice") -> None:
        """A review: APPROVED, CHANGES_REQUESTED, COMMENTED or DISMISSED."""
        with self._lock:
            self._next_id += 1
            pr = self.pulls[number]
            pr.reviews.append({"id": self._next_id, "user": {"login": reviewer}, "state": state,
                               "body": body, "submitted_at": _now()})
            if reviewer in pr.requested_reviewers:
                pr.requested_reviewers.remove(reviewer)
        self.send_webhook("pull_request_review", {"action": "submitted", "pull_request": {"number": number}})

    def merge(self, number: int) -> None:
        with self._lock:
            pr = self.pulls[number]
            pr.state = "closed"
            pr.merged_at = _now()
        self.send_webhook("pull_request", {"action": "closed", "pull_request": {"number": number}})

    def close(self, number: int) -> None:
        """Closed without merging."""
        with self._lock:
            self.pulls[number].state = "closed"
        self.send_webhook("pull_request", {"action": "closed", "pull_request": {"number": number}})

    def reopen(self, number: int) -> None:
        with self._lock:
            self.pulls[number].state = "open"
        self.send_webhook("pull_request", {"action": "reopened", "pull_request": {"number": number}})

    def set_conflicting(self, number: int, conflicting: bool = True) -> None:
        """GitHub works out the branch no longer merges (main moved into it)."""
        with self._lock:
            self.pulls[number].conflicting = conflicting
        self.send_webhook("pull_request", {"action": "synchronize", "pull_request": {"number": number}})

    def set_unresolved(self, number: int, threads: int) -> None:
        with self._lock:
            self.pulls[number].unresolved_threads = threads
        self.send_webhook("pull_request_review_thread", {"action": "resolved", "pull_request": {"number": number}})

    def commit(self, branch: str, message: str, author: str = "Gus Human") -> str:
        """A person pushes a commit to a branch on GitHub; its sha. A push to
        a pull request's branch is a `synchronize`."""
        tree = self._git_out("rev-parse", f"refs/heads/{branch}^{{tree}}")
        email = author.split()[0].lower() + "@example.com"
        env = {"GIT_AUTHOR_NAME": author, "GIT_AUTHOR_EMAIL": email, "GIT_COMMITTER_NAME": author,
               "GIT_COMMITTER_EMAIL": email}
        sha = self._git_out("commit-tree", tree, "-p", f"refs/heads/{branch}", "-m", message, env=env)
        self._git_out("update-ref", f"refs/heads/{branch}", sha)
        for pr in list(self.pulls.values()):
            if pr.head == branch and pr.state == "open":
                self.send_webhook("pull_request", {"action": "synchronize", "pull_request": {"number": pr.number}})
        return sha

    def _git_out(self, *args: str, env: dict | None = None) -> str:
        import os

        result = subprocess.run(["git", *args], cwd=self.bare, capture_output=True, text=True, check=True,
                                env={**os.environ, **(env or {})})
        return result.stdout.strip()

    def set_check(self, number: int, name: str, status: str = "completed", conclusion: str | None = "success",
                  kind: str = "check_run") -> None:
        """CI reports on the pull request's head, by name: a check run
        (`check_run`, then its suite) or a commit status (`status`) — and
        tells dude by the webhook each sends."""
        sha = self.branch_sha(self.pulls[number].head)
        with self._lock:
            checks = self.checks.setdefault(sha, {})
            prior = checks.get(name)
            self._next_id += 1
            checks[name] = {"id": prior["id"] if prior else self._next_id, "name": name, "kind": kind,
                            "status": status, "conclusion": conclusion if status == "completed" else None,
                            "started_at": _now(), "completed_at": _now() if status == "completed" else None}
        if kind == "status":
            state = "pending" if status != "completed" else conclusion
            self.send_webhook("status", {"sha": sha, "state": state, "context": name})
        else:
            self.send_webhook("check_run", {"action": status, "check_run": {"head_sha": sha, "name": name}})
            if status == "completed":
                self.send_webhook("check_suite", {"action": "completed", "check_suite": {"head_sha": sha}})

    def open_pull(self, head: str) -> str:
        """A person's pull request from a new branch off main, opened before
        dude was watching (no webhook); its head commit."""
        self._git_out("update-ref", f"refs/heads/{head}", self.branch_sha("main"))
        sha = self.commit(head, f"work on {head}")
        with self._lock:
            number = len(self.pulls) + 1
            self.pulls[number] = PullRequest(number=number, head=head, base="main", title=head, body="", draft=False)
        return sha

    def add_check_run(self, sha: str, name: str = "ci") -> None:
        """A completed check run on a commit, with no webhook: CI that ran
        before dude was looking."""
        with self._lock:
            self._next_id += 1
            self.checks.setdefault(sha, {})[name] = {
                "id": self._next_id, "name": name, "kind": "check_run", "status": "completed",
                "conclusion": "success", "started_at": _now(), "completed_at": _now()}

    def send_webhook(self, event: str, payload: dict, secret: str | None = None) -> int:
        """Deliver a signed webhook to dude, as GitHub would. Returns the status."""
        import requests

        # A sibling repository delivers where its owner's does.
        url = self._parent.webhook_url if self._parent else self.webhook_url
        secret_value = self._parent.webhook_secret if self._parent else self.webhook_secret
        if not url:
            return 0
        body = json.dumps({**payload, "repository": {"full_name": f"{self.owner}/{self.repo}"}}).encode()
        key = (secret if secret is not None else secret_value or "").encode()
        signature = "sha256=" + hmac.new(key, body, hashlib.sha256).hexdigest()
        resp = requests.post(url, data=body, timeout=10, headers={
            "content-type": "application/json",
            "x-github-event": event,
            "x-github-delivery": str(uuid.uuid4()),
            "x-hub-signature-256": signature,
        })
        self.deliveries.append((event, resp.status_code))
        return resp.status_code

    # -- the API -------------------------------------------------------------

    def _handler(self):
        root = self

        class Handler(BaseHTTPRequestHandler):
            @property
            def github(self) -> "FakeGitHub":
                return root._for_path(self.path)

            def log_message(self, *args):  # quiet
                pass

            def _token(self) -> str:
                auth = self.headers.get("authorization") or ""
                scheme, _, value = auth.partition(" ")
                if scheme == "Basic":
                    return base64.b64decode(value).decode(errors="replace").partition(":")[2]
                return value if scheme in ("Bearer", "token") else ""

            def _profile(self) -> TokenProfile | None:
                return root.tokens.get(self._token())

            def _send(self, status: int, body, headers: dict[str, str] | None = None) -> None:
                data = json.dumps(body).encode()
                self.send_response(status)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(data)))
                # A classic token's scopes come back on every answer.
                profile = self._profile()
                if profile and profile.scopes is not None:
                    self.send_header("x-oauth-scopes", profile.scopes)
                for name, value in (headers or {}).items():
                    self.send_header(name, value)
                self.end_headers()
                self.wfile.write(data)

            def _refuse(self, permission: str) -> None:
                """A fine-grained token without the permission, as GitHub refuses it."""
                self._send(403, {"message": "Resource not accessible by personal access token"},
                           {"x-accepted-github-permissions": permission})

            def _rate_limited(self, endpoint: str) -> bool:
                profile = self._profile()
                if profile and endpoint in profile.rate_limited:
                    self._send(403, {"message": "API rate limit exceeded for user ID 1."},
                               {"x-ratelimit-remaining": "0", "x-ratelimit-reset": str(int(time.time()) + 600)})
                    return True
                return False

            def _pull_json(self, pr: PullRequest) -> dict:
                return {
                    "number": pr.number,
                    "node_id": f"PR_{pr.number}",
                    "html_url": f"https://github.test/{self.github.owner}/{self.github.repo}/pull/{pr.number}",
                    "draft": pr.draft,
                    "state": pr.state,
                    "merged_at": pr.merged_at,
                    "head": {"sha": self.github.branch_sha(pr.head) or "0" * 40},
                    "base": {"ref": pr.base},
                    "mergeable": not pr.conflicting,
                    "mergeable_state": "dirty" if pr.conflicting else "clean",
                    "requested_reviewers": [{"login": login} for login in pr.requested_reviewers],
                }

            def _body(self) -> dict:
                length = int(self.headers.get("content-length") or 0)
                return json.loads(self.rfile.read(length) or b"{}") if length else {}

            def _git(self, *args: str) -> subprocess.CompletedProcess:
                return subprocess.run(["git", *args], cwd=self.github.bare, capture_output=True, text=True)

            def do_PATCH(self) -> None:
                if m := re.fullmatch(rf"/repos/{self.github.owner}/{self.github.repo}/hooks/(\d+)", self.path):
                    body = self._body()
                    with self.github._lock:
                        for hook in self.github.hooks:
                            if hook["id"] == int(m[1]):
                                hook.update(body)
                                return self._send(200, hook)
                    return self._send(404, {"message": "Not Found"})
                prefix = f"/repos/{self.github.owner}/{self.github.repo}/git/refs/heads/"
                if not self.path.startswith(prefix):
                    return self._send(404, {"message": "Not Found"})
                branch, body = self.path[len(prefix):], self._body()
                current = self.github.branch_sha(branch)
                if not current:
                    return self._send(422, {"message": "Reference does not exist"})
                # GitHub refuses a non-forced update that is not a
                # fast-forward; that refusal is what protects a person's
                # commits, so the fake keeps it.
                if not body.get("force") and self._git("merge-base", "--is-ancestor", current, body["sha"]).returncode:
                    return self._send(422, {"message": "Update is not a fast forward"})
                self._git("update-ref", f"refs/heads/{branch}", body["sha"])
                self._send(200, {"ref": f"refs/heads/{branch}"})

            def do_PUT(self) -> None:
                gh, body = self.github, self._body()
                prefix = f"/repos/{gh.owner}/{gh.repo}"
                if m := re.fullmatch(rf"{prefix}/pulls/(\d+)/merge", self.path):
                    pr = gh.pulls[int(m[1])]
                    if pr.state != "open":
                        return self._send(405, {"message": "Pull Request is not mergeable"})
                    if body.get("sha") and body["sha"] != gh.branch_sha(pr.head):
                        return self._send(409, {"message": "Head branch was modified. Review and try the merge again."})
                    if pr.conflicting:
                        return self._send(405, {"message": "Pull Request is not mergeable"})
                    # The head's commits onto the base, as a merge would.
                    self._git("update-ref", f"refs/heads/{pr.base}", gh.branch_sha(pr.head))
                    with gh._lock:
                        gh.merges.append({"number": pr.number, "method": body.get("merge_method")})
                        pr.state, pr.merged_at = "closed", _now()
                    self._send(200, {"merged": True, "sha": gh.branch_sha(pr.head), "message": "Pull Request successfully merged"})
                    gh.send_webhook("pull_request", {"action": "closed", "pull_request": {"number": pr.number}})
                    return None
                if m := re.fullmatch(rf"{prefix}/pulls/(\d+)/update-branch", self.path):
                    pr = gh.pulls[int(m[1])]
                    with gh._lock:
                        gh.updates.append(pr.number)
                    if pr.conflicting:
                        return self._send(422, {"message": "merge conflict between base and head"})
                    if body.get("expected_head_sha") and body["expected_head_sha"] != gh.branch_sha(pr.head):
                        return self._send(422, {"message": "expected head sha didn't match current head ref"})
                    # A merge commit of the base into the head, as GitHub makes.
                    tree = self._git("rev-parse", f"refs/heads/{pr.head}^{{tree}}").stdout.strip()
                    sha = gh._git_out("commit-tree", tree, "-p", f"refs/heads/{pr.head}", "-p", f"refs/heads/{pr.base}",
                                      "-m", f"Merge branch '{pr.base}' into {pr.head}",
                                      env={"GIT_AUTHOR_NAME": "GitHub", "GIT_AUTHOR_EMAIL": "noreply@github.com",
                                           "GIT_COMMITTER_NAME": "GitHub", "GIT_COMMITTER_EMAIL": "noreply@github.com"})
                    self._git("update-ref", f"refs/heads/{pr.head}", sha)
                    self._send(202, {"message": "Updating pull request branch."})
                    gh.send_webhook("pull_request", {"action": "synchronize", "pull_request": {"number": pr.number}})
                    return None
                self._send(404, {"message": "Not Found"})

            def do_DELETE(self) -> None:
                prefix = f"/repos/{self.github.owner}/{self.github.repo}/git/refs/heads/"
                if not self.path.startswith(prefix) or self._git("update-ref", "-d", f"refs/heads/{self.path[len(prefix):]}").returncode:
                    return self._send(422, {"message": "Reference does not exist"})
                self.send_response(204)
                self.end_headers()

            def do_POST(self) -> None:
                body = self._body()
                if self.path == "/graphql":
                    return self._graphql(body)
                gh = self.github
                prefix = f"/repos/{gh.owner}/{gh.repo}"
                if m := re.fullmatch(rf"{prefix}/check-runs/(\d+)/rerequest", self.path):
                    # Only the app that owns a check run may re-request it;
                    # a token may not, as on GitHub.
                    return self._send(403, {"message": "Invalid OAuth application client_id or secret."})
                if m := re.fullmatch(rf"{prefix}/actions/jobs/(\d+)/rerun", self.path):
                    with gh._lock:
                        gh.jobs_rerun.append(int(m[1]))
                    return self._send(201, {})
                if m := re.fullmatch(rf"{prefix}/pulls/(\d+)/requested_reviewers", self.path):
                    pr = gh.pulls[int(m[1])]
                    with gh._lock:
                        gh.review_requests.append((pr.number, list(body.get("reviewers", []))))
                        pr.requested_reviewers += [r for r in body.get("reviewers", []) if r not in pr.requested_reviewers]
                    return self._send(201, self._pull_json(pr))
                # A comment the token posts, as its login: on the conversation,
                # or a reply in a line comment's thread. GitHub then tells
                # dude by webhook, as it does of anyone's comment.
                m = re.fullmatch(rf"{prefix}/issues/(\d+)/comments", self.path)
                reply = re.fullmatch(rf"{prefix}/pulls/(\d+)/comments/(\d+)/replies", self.path)
                if m or reply:
                    number = int((m or reply)[1])
                    pr = gh.pulls[number]
                    with gh._lock:
                        parent = next((c for c in pr.comments if reply and c["id"] == int(reply[2]) and c["path"]), None)
                        if reply and parent is None:
                            return self._send(404, {"message": "Not Found"})
                        gh._next_id += 1
                        anchor = "discussion_r" if reply else "issuecomment-"
                        comment = {"id": gh._next_id, "body": body.get("body", ""), "user": {"login": gh.token_login},
                                   "created_at": _now(), "path": parent["path"] if parent else None,
                                   "in_reply_to_id": int(reply[2]) if reply else None,
                                   "html_url": f"https://github.test/{gh.owner}/{gh.repo}/pull/{number}#{anchor}{gh._next_id}"}
                        pr.comments.append(comment)
                    if reply:
                        gh.send_webhook("pull_request_review_comment", {"action": "created", "pull_request": {"number": number}})
                    else:
                        gh.send_webhook("issue_comment", {"action": "created", "issue": {"number": number, "pull_request": {"url": ""}}})
                    return self._send(201, comment)
                if self.path == f"/repos/{self.github.owner}/{self.github.repo}/git/refs":
                    if self._git("update-ref", body["ref"], body["sha"], "").returncode:
                        return self._send(422, {"message": "Reference already exists"})
                    return self._send(201, {"ref": body["ref"]})
                if self.path == f"/repos/{self.github.owner}/{self.github.repo}/hooks":
                    if not (self._profile() or TokenProfile()).hooks_write:
                        return self._refuse("repository_hooks=write")
                    if not (body.get("config") or {}).get("url"):
                        return self._send(422, {"message": "Validation Failed",
                                                "errors": [{"resource": "Hook", "code": "custom", "message": "Config must contain URL."}]})
                    with self.github._lock:
                        hook = {**body, "id": len(self.github.hooks) + 1}
                        self.github.hooks.append(hook)
                    return self._send(201, hook)
                if re.fullmatch(rf"/repos/{self.github.owner}/{self.github.repo}/pulls", self.path):
                    if not (self._profile() or TokenProfile()).pulls_write:
                        return self._refuse("pull_requests=write")
                    if not body.get("head") or not body.get("base"):
                        return self._send(422, {"message": "Validation Failed", "errors": [
                            {"resource": "PullRequest", "code": "missing_field", "field": "base, head"}]})
                    if not self.github.branch_sha(body["head"]):
                        return self._send(422, {"message": f"No commits on {body['head']}"})
                    with self.github._lock:
                        if any(p.head == body["head"] and p.state == "open" for p in self.github.pulls.values()):
                            return self._send(422, {"message": "A pull request already exists"})
                        number = len(self.github.pulls) + 1
                        pr = PullRequest(
                            number=number, head=body["head"], base=body["base"],
                            title=body["title"], body=body.get("body", ""),
                            draft=bool(body.get("draft")),
                        )
                        self.github.pulls[number] = pr
                    return self._send(201, self._pull_json(pr))
                self._send(404, {"message": "Not Found"})

            def _graphql(self, body: dict) -> None:
                """The queries dude sends: a pull request's review threads,
                and who could review it (REVIEWERS; the first two suggested)."""
                v = body.get("variables") or {}
                gh = root._for_repo(v.get("name", "")) if v.get("owner") == root.owner else None
                if "teams(" in body.get("query", ""):
                    # The owner's teams: none, as for a repository a user owns.
                    return self._send(200, {"data": {"organization": None}, "errors": [
                        {"type": "NOT_FOUND", "message": "Could not resolve to an Organization"}]})
                if gh is not None and "suggestedReviewers" in body.get("query", ""):
                    pr = gh.pulls.get(int(v.get("number") or 0))
                    words = (v.get("q") or "").lower()
                    # Suggestions only without words, as GitHub's @include(if:$suggest).
                    suggested = [] if words else [{"isCommenter": i == 1, "reviewer": u} for i, u in enumerate(REVIEWERS[:2])]
                    return self._send(200, {"data": {"repository": {
                        "pullRequest": {"author": {"login": "dude-bot"}, "suggestedReviewers": suggested} if pr else None,
                        "assignableUsers": {"nodes": [u for u in REVIEWERS if words in f"{u['login']} {u['name']}".lower()]}}}})
                pr = gh.pulls.get(int(v.get("number") or 0)) if gh else None
                if pr is None:
                    return self._send(200, {"data": {"repository": None}, "errors": [
                        {"type": "NOT_FOUND", "message": "Could not resolve to a Repository"}]})
                nodes = [{"isResolved": True}] + [{"isResolved": False}] * pr.unresolved_threads
                self._send(200, {"data": {"repository": {"pullRequest": {"reviewThreads": {
                    "nodes": nodes, "pageInfo": {"hasNextPage": False, "endCursor": None}}}}}})

            def _checks(self, sha: str, kind: str) -> list[dict]:
                with self.github._lock:
                    return [dict(c) for c in self.github.checks.get(sha, {}).values() if c["kind"] == kind]

            def do_GET(self) -> None:
                path, _, query = self.path.partition("?")
                if m := re.fullmatch(rf"/{root.owner}/([^/]+)\.git/info/refs", path):
                    if m[1] != root.repo and m[1] not in root.siblings:
                        return self._send(404, {"message": "Not Found"})
                    with root._lock:
                        root.receive_requests.append(self.path)
                    profile = self._profile() if (self.headers.get("authorization") or "").startswith("Basic ") else None
                    if profile is None:
                        return self._send(401, {"message": "Bad credentials"})
                    if query != "service=git-receive-pack":
                        return self._send(400, {"message": "Expected receive-pack discovery"})
                    if not profile.push:
                        return self._send(403, {"message": "Write access to repository not granted."})
                    data = b"001f# service=git-receive-pack\n0000"
                    self.send_response(200)
                    self.send_header("content-type", "application/x-git-receive-pack-advertisement")
                    self.send_header("content-length", str(len(data)))
                    self.end_headers()
                    self.wfile.write(data)
                    return
                since = None
                if m := re.search(r"since=([^&]+)", query):
                    from urllib.parse import unquote
                    since = unquote(m[1])

                def after_since(items: list[dict]) -> list[dict]:
                    # GitHub's `since` is inclusive; the factory's cursor
                    # handling has to cope with that, so the fake keeps it.
                    return [c for c in items if not since or c["created_at"] >= since]

                prefix = f"/repos/{self.github.owner}/{self.github.repo}"
                if path == "/user":
                    # Who the token is. The fixture accepts only the tokens it knows.
                    if self._profile() is None or not (self.headers.get("authorization") or "").startswith("Bearer "):
                        return self._send(401, {"message": "Bad credentials"})
                    return self._send(200, {"login": "dude-bot"})
                if path == prefix:
                    return self._send(200, {"full_name": f"{self.github.owner}/{self.github.repo}", "private": True,
                                            "default_branch": "main", "owner": {"login": self.github.owner, "type": root.owner_type}})
                if path == f"{prefix}/pulls":
                    from urllib.parse import parse_qs
                    q = {k: v[0] for k, v in parse_qs(query).items()}
                    head = q.get("head", "").partition(":")[2]
                    with self.github._lock:
                        pulls = [p for p in sorted(self.github.pulls.values(), key=lambda p: -p.number)
                                 if q.get("state", "open") in ("all", p.state) and (not head or p.head == head)
                                 and (not q.get("base") or p.base == q["base"])]
                    return self._send(200, [self._pull_json(pr) for pr in pulls])
                if path == f"{prefix}/actions/runs":
                    # A workflow run per commit with check runs: what Actions reported on.
                    with self.github._lock:
                        shas = [sha for sha, checks in self.github.checks.items()
                                if any(c["kind"] == "check_run" for c in checks.values())]
                    runs = [{"id": i + 1, "head_sha": sha} for i, sha in enumerate(reversed(shas))]
                    return self._send(200, {"total_count": len(runs), "workflow_runs": runs})
                if path == f"{prefix}/hooks":
                    return self._send(200, self.github.hooks)
                if m := re.fullmatch(rf"{prefix}/compare/([^.]+)\.\.\.(.+)", path):
                    diff = self._git("diff", "--numstat", m[1], m[2])
                    if diff.returncode:
                        return self._send(404, {"message": "Not Found"})
                    files = []
                    for line in diff.stdout.splitlines():
                        parts = line.split("\t", 2)
                        if len(parts) == 3:
                            files.append({"filename": parts[2], "additions": int(parts[0]) if parts[0].isdigit() else 0,
                                          "deletions": int(parts[1]) if parts[1].isdigit() else 0})
                    # Commits on the base the head lacks: how far behind it is; and the reverse.
                    behind = self._git("rev-list", "--count", f"{m[2]}..{m[1]}").stdout.strip()
                    ahead = self._git("rev-list", "--count", f"{m[1]}..{m[2]}").stdout.strip()
                    return self._send(200, {"files": files, "behind_by": int(behind or 0), "ahead_by": int(ahead or 0)})
                if m := re.fullmatch(rf"{prefix}/pulls/(\d+)", path):
                    return self._send(200, self._pull_json(self.github.pulls[int(m[1])]))
                if m := re.fullmatch(rf"/orgs/([^/]+)/members/([^/]+)", path):
                    if m[2] not in root.members:
                        return self._send(404, {"message": "Not Found"})
                    self.send_response(204)
                    return self.end_headers()
                if m := re.fullmatch(rf"{prefix}/collaborators/([^/]+)/permission", path):
                    permission = self.github.permissions.get(m[1], "write")
                    if permission == "none":
                        return self._send(404, {"message": f"{m[1]} is not a user"})
                    return self._send(200, {"permission": permission, "role_name": permission})
                if m := re.fullmatch(rf"{prefix}/commits/([0-9a-f]+)", path):
                    author = self._git("log", "-1", "--format=%an", m[1])
                    if author.returncode:
                        return self._send(404, {"message": "No commit found"})
                    return self._send(200, {"sha": m[1], "author": None,
                                            "commit": {"author": {"name": author.stdout.strip()}}})
                if m := re.fullmatch(rf"{prefix}/commits/([^/]+)/check-runs", path):
                    if self._rate_limited("check-runs"):
                        return None
                    runs = [{"id": c["id"], "name": c["name"], "status": c["status"], "conclusion": c["conclusion"],
                             "html_url": f"https://github.test/{self.github.owner}/{self.github.repo}/runs/{c['id']}",
                             "started_at": c["started_at"], "completed_at": c["completed_at"], "app": {"slug": "github-actions"}}
                            for c in self._checks(m[1], "check_run")]
                    # GitHub refuses a token without check-run access only
                    # when there are runs to show; with none it answers 0.
                    if runs and not (self._profile() or TokenProfile()).checks:
                        return self._refuse("checks=read")
                    return self._send(200, {"total_count": len(runs), "check_runs": runs})
                if m := re.fullmatch(rf"{prefix}/commits/([^/]+)/status", path):
                    statuses = [{"context": c["name"], "state": c["conclusion"] or "pending",
                                 "target_url": f"https://ci.test/{c['id']}", "created_at": c["started_at"],
                                 "updated_at": c["completed_at"] or c["started_at"]}
                                for c in self._checks(m[1], "status")]
                    # GitHub's combined state: failure over pending over
                    # success; with no statuses at all, pending and a count of 0.
                    states = {st["state"] for st in statuses}
                    combined = next((x for x in ("failure", "error", "pending") if x in states), "success" if states else "pending")
                    return self._send(200, {"state": combined, "total_count": len(statuses), "statuses": statuses})
                if m := re.fullmatch(rf"{prefix}/check-runs/(\d+)", path):
                    return self._send(200, {"id": int(m[1]), "output": {
                        "title": "1 test failed", "summary": "test_greeting: expected hello, got hi", "text": ""}})
                if m := re.fullmatch(rf"{prefix}/check-runs/(\d+)/annotations", path):
                    return self._send(200, [{"path": "greet.py", "start_line": 12, "annotation_level": "failure",
                                             "message": "expected hello"}])
                if m := re.fullmatch(rf"{prefix}/pulls/(\d+)/reviews", path):
                    return self._send(200, self.github.pulls[int(m[1])].reviews)
                if m := re.fullmatch(rf"{prefix}/issues/(\d+)/comments", path):
                    comments = [c for c in self.github.pulls[int(m[1])].comments if not c["path"]]
                    return self._send(200, after_since(comments))
                if m := re.fullmatch(rf"{prefix}/pulls/(\d+)/comments", path):
                    comments = [c for c in self.github.pulls[int(m[1])].comments if c["path"]]
                    return self._send(200, after_since(comments))
                self._send(404, {"message": "Not Found"})

        return Handler
