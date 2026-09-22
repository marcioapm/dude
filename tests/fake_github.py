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
leaving a comment, merging — through `FakeGitHub` directly.
"""

from __future__ import annotations

import json
import re
import socket
import subprocess
import threading
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


def _now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


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
    checks: str = "success"
    check_count: int = 0


class FakeGitHub:
    """A bare repository, a git daemon serving it, and an API in front of both."""

    def __init__(self, root: Path, owner: str = "acme", repo: str = "target") -> None:
        self.owner = owner
        self.repo = repo
        self.root = root
        self.bare = root / owner / f"{repo}.git"
        self.pulls: dict[int, PullRequest] = {}
        self._next_id = 1000
        self._lock = threading.Lock()
        self._daemon: subprocess.Popen | None = None
        self._server: ThreadingHTTPServer | None = None
        self.git_port = _free_port()
        self.api_port = _free_port()

    # -- lifecycle -----------------------------------------------------------

    def start(self) -> None:
        self._seed()
        self._daemon = subprocess.Popen(
            [
                "git", "daemon",
                f"--base-path={self.root}",
                f"--port={self.git_port}",
                "--export-all",
                # The runner pushes; git daemon refuses that unless asked.
                "--enable=receive-pack",
                "--reuseaddr",
                "--listen=127.0.0.1",
                str(self.root),
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        self._server = ThreadingHTTPServer(("127.0.0.1", self.api_port), self._handler())
        threading.Thread(target=self._server.serve_forever, daemon=True).start()
        self._wait_for_daemon()

    def stop(self) -> None:
        if self._server:
            self._server.shutdown()
        if self._daemon:
            self._daemon.terminate()
            self._daemon.wait(timeout=5)

    @property
    def clone_url(self) -> str:
        return f"git://127.0.0.1:{self.git_port}/{self.owner}/{self.repo}.git"

    @property
    def api_url(self) -> str:
        return f"http://127.0.0.1:{self.api_port}"

    def _seed(self) -> None:
        seed = self.root / "seed"
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

    def branch_log(self, branch: str) -> list[str]:
        result = subprocess.run(
            ["git", "log", "--format=%s", f"refs/heads/{branch}"],
            cwd=self.bare, capture_output=True, text=True,
        )
        return [line for line in result.stdout.splitlines() if line]

    def comment(self, number: int, body: str, author: str = "reviewer", path: str | None = None):
        with self._lock:
            self._next_id += 1
            self.pulls[number].comments.append(
                {"id": self._next_id, "body": body, "user": {"login": author},
                 "created_at": _now(), "path": path}
            )

    def merge(self, number: int) -> None:
        with self._lock:
            pr = self.pulls[number]
            pr.state = "closed"
            pr.merged_at = _now()

    # -- the API -------------------------------------------------------------

    def _handler(self):
        github = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):  # quiet
                pass

            def _send(self, status: int, body) -> None:
                data = json.dumps(body).encode()
                self.send_response(status)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def _pull_json(self, pr: PullRequest) -> dict:
                return {
                    "number": pr.number,
                    "node_id": f"PR_{pr.number}",
                    "html_url": f"https://github.test/{github.owner}/{github.repo}/pull/{pr.number}",
                    "draft": pr.draft,
                    "state": pr.state,
                    "merged_at": pr.merged_at,
                    "head": {"sha": github.branch_sha(pr.head) or "0" * 40},
                }

            def do_POST(self) -> None:
                body = json.loads(self.rfile.read(int(self.headers["content-length"])) or b"{}")
                if re.fullmatch(rf"/repos/{github.owner}/{github.repo}/pulls", self.path):
                    if not github.branch_sha(body["head"]):
                        return self._send(422, {"message": f"No commits on {body['head']}"})
                    with github._lock:
                        if any(p.head == body["head"] and p.state == "open" for p in github.pulls.values()):
                            return self._send(422, {"message": "A pull request already exists"})
                        number = len(github.pulls) + 1
                        pr = PullRequest(
                            number=number, head=body["head"], base=body["base"],
                            title=body["title"], body=body.get("body", ""),
                            draft=bool(body.get("draft")),
                        )
                        github.pulls[number] = pr
                    return self._send(201, self._pull_json(pr))
                self._send(404, {"message": "Not Found"})

            def do_GET(self) -> None:
                path, _, query = self.path.partition("?")
                since = None
                if m := re.search(r"since=([^&]+)", query):
                    from urllib.parse import unquote
                    since = unquote(m[1])

                def after_since(items: list[dict]) -> list[dict]:
                    # GitHub's `since` is inclusive; the factory's cursor
                    # handling has to cope with that, so the fake keeps it.
                    return [c for c in items if not since or c["created_at"] >= since]

                prefix = f"/repos/{github.owner}/{github.repo}"
                if m := re.fullmatch(rf"{prefix}/pulls/(\d+)", path):
                    return self._send(200, self._pull_json(github.pulls[int(m[1])]))
                if re.fullmatch(rf"{prefix}/commits/[^/]+/status", path):
                    # No CI in the fixture: GitHub reports zero statuses.
                    return self._send(200, {"state": "pending", "total_count": 0})
                if m := re.fullmatch(rf"{prefix}/pulls/(\d+)/reviews", path):
                    return self._send(200, github.pulls[int(m[1])].reviews)
                if m := re.fullmatch(rf"{prefix}/issues/(\d+)/comments", path):
                    comments = [c for c in github.pulls[int(m[1])].comments if not c["path"]]
                    return self._send(200, after_since(comments))
                if m := re.fullmatch(rf"{prefix}/pulls/(\d+)/comments", path):
                    comments = [c for c in github.pulls[int(m[1])].comments if c["path"]]
                    return self._send(200, after_since(comments))
                self._send(404, {"message": "Not Found"})

        return Handler
