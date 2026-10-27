"""TestEnvironment — isolated per-run environment for the E2E suite.

Each run gets its own PostgreSQL database, its own bucket on a shared S3
(versitygw, as lux's suite uses) and free ports, so suites can run
concurrently and a failed run leaves no residue in a shared service.

The suite drives the factory through its public HTTP API only. It never
imports the Bun implementation, which is what makes it a real test of the
deployed system rather than of internal functions (plan §29, §50).
"""

from __future__ import annotations

import fcntl
import json
import os
import re
import socket
import sys
import signal
import subprocess
import threading
import time
import uuid
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import boto3
import psycopg
import requests


REPO_ROOT = Path(__file__).resolve().parent.parent

# Photos go to object storage. The suite shares one S3 gateway over a
# local directory, started on first use and left running like the dev
# Postgres; each run makes and removes its own bucket.
# Fully qualified: Docker reads it as the short name, and podman, with no
# unqualified-search registries configured, can pull it at all.
S3_IMAGE = os.environ.get("DUDE_TEST_S3_IMAGE", "docker.io/versity/versitygw:v1.7.0")
S3_CONTAINER = os.environ.get("DUDE_TEST_S3_CONTAINER", "dude-e2e-s3")
S3_PORT = int(os.environ.get("DUDE_TEST_S3_PORT", "59200"))
S3_ACCESS_KEY = "dudes3"
S3_SECRET_KEY = "dudes3-secret"
S3_REGION = "us-east-1"

# The owner role runs migrations; the app role serves traffic. They are
# deliberately different: the app role has neither SUPERUSER nor BYPASSRLS,
# so row-level security is a real boundary rather than a convention.
POSTGRES_HOST = os.environ.get("DUDE_TEST_PG_HOST", "localhost")
POSTGRES_PORT = int(os.environ.get("DUDE_TEST_PG_PORT", "5433"))
POSTGRES_OWNER = os.environ.get("DUDE_TEST_PG_OWNER", "dude")
POSTGRES_OWNER_PASSWORD = os.environ.get("DUDE_TEST_PG_OWNER_PASSWORD", "dude")
POSTGRES_APP_USER = os.environ.get("DUDE_TEST_PG_APP_USER", "dude_app")
POSTGRES_APP_PASSWORD = os.environ.get("DUDE_TEST_PG_APP_PASSWORD", "dude_app")
POSTGRES_ADMIN_DB = os.environ.get("DUDE_TEST_PG_ADMIN_DB", "postgres")
# The suite's Slack app, as the fake Slack and the orchestrator both know it.
SLACK_SIGNING_SECRET = "e2e-slack-signing-secret"
SLACK_CLIENT_ID = "e2e-slack-client"
SLACK_CLIENT_SECRET = "e2e-slack-client-secret"

# The dude layer library images are finished with. The fake lux runs any
# ref, so this one is never pulled; set, the image library is on. An
# organization that names no library image runs as it did before it.
TEST_LAYER = "registry.test/dude/layer@sha256:" + "a" * 64

# The backend stores photos in versitygw, which closes the connection after
# each PUT; Bun before this reports that as ConnectionClosed and fails the
# upload (apps/control-plane/src/storage.ts, MIN_BUN_FOR_S3).
MIN_BUN = (1, 4, 0)

# The fake lux's preview domain: its servers' URLs are under it.
FAKE_PREVIEW_DOMAIN = "preview.dude.test"
FAKE_PUBLIC_URL = "https://dude.e2e.test"

# Access mode's team and application audience: what the control plane's own
# Access tests sign for (apps/control-plane/test/access-harness.ts), which
# tests/access-edge.ts reuses.
ACCESS_TEAM = "dudetest"
ACCESS_AUD = "app-audience"


@dataclass
class AccessMode:
    """A backend behind (a stand-in for) Cloudflare Access: its URL, and tokens for any email."""

    url: str
    edge_url: str
    designation: str

    def token(self, email: str, name: str | None = None) -> str:
        """A token the team signed for `email`; `name` is what get-identity says for it."""
        resp = requests.post(f"{self.edge_url}/test/token", json={"email": email, **({"name": name} if name else {})},
                             headers={"x-test-designation": self.designation}, timeout=10)
        resp.raise_for_status()
        return resp.json()["token"]

    def session(self, email: str, name: str | None = None) -> requests.Session:
        """Requests as a browser signed in as `email` sends them: the cookie, and this site's Origin."""
        s = requests.Session()
        s.headers.update({"cookie": f"CF_Authorization={self.token(email, name)}", "origin": self.url, "content-type": "application/json"})
        s.request = _prefixed(s.request, self.url)  # type: ignore[method-assign]
        return s

    def sign_in(self, page, email: str, name: str | None = None, at: str = "/") -> None:
        """The browser of `page` signed in to Access as `email` (replacing whoever was), opening `at`."""
        page.context.clear_cookies()
        page.context.add_cookies([{"name": "CF_Authorization", "value": self.token(email, name), "url": self.url}])
        page.goto(self.url + at)


def _prefixed(request, base: str):
    def call(method, url, *args, **kwargs):
        kwargs.setdefault("timeout", 30)
        return request(method, url if url.startswith("http") else base + url, *args, **kwargs)
    return call


def bun_problem(version: str) -> str | None:
    """Why the `bun --version` output `version` cannot run the suite, or None."""
    version = version.strip()
    need = ".".join(map(str, MIN_BUN))
    parts = version.split("-")[0].split("+")[0].split(".")
    if len(parts) != 3 or not all(p.isdigit() for p in parts):
        return f"could not read the Bun version from {version!r}; the suite needs Bun >= {need}"
    have = tuple(int(p) for p in parts)
    if have < MIN_BUN or (have == MIN_BUN and "-" in version.split("+")[0]):
        return (f"Bun {version} is on PATH; the suite needs Bun >= {need}: earlier Bun fails every "
                "photo upload to the test S3 (versitygw answers Connection: close)")
    return None


def require_bun() -> None:
    """Stop before building anything when the `bun` on PATH is too old."""
    try:
        out = subprocess.run(["bun", "--version"], capture_output=True, text=True, check=True).stdout
    except (OSError, subprocess.CalledProcessError) as err:
        raise SystemExit(f"bun --version failed: {err}")
    problem = bun_problem(out)
    if problem:
        raise SystemExit(problem)


def lux_env() -> dict:
    """The lux environment to run the contract suite against.

    DUDE_TEST_LUX_ENV names its env.json; otherwise the most recent one
    lux's `run_tests.py --serve` wrote. Fails loudly when there is none,
    because --lux was asked for.
    """
    import glob
    import json

    path = os.environ.get("DUDE_TEST_LUX_ENV")
    if not path:
        found = sorted(glob.glob("/tmp/lux-e2e-*/env.json"), key=os.path.getmtime, reverse=True)
        found = [p for p in found if json.loads(Path(p).read_text()).get("api_key")]
        if not found:
            raise SystemExit("no lux environment: run `uv run python run_tests.py --serve --detach` in lux/tests")
        path = found[0]
    env = json.loads(Path(path).read_text())
    try:
        requests.get(env["luxd_url"] + "/health", timeout=3).raise_for_status()
    except requests.RequestException as err:
        raise SystemExit(f"lux at {env['luxd_url']} is not answering: {err}")
    return env


CHECKS_NOTICES = REPO_ROOT / "orchestrator" / "internal" / "delivery" / "agents" / "notices" / "checks.md"


def _checks_questions() -> list[str]:
    """The fixed opening of the checks gate's Backed, Missing and pre-existing
    questions: each template's text up to its first action, as the
    orchestrator sends it."""
    text = CHECKS_NOTICES.read_text()
    out = []
    for name in ("notices/checks/backed", "notices/checks/missing", "notices/checks/preexisting"):
        start = text.index('{{define "' + name + '"}}') + len('{{define "' + name + '"}}')
        out.append(text[start:text.index("{{", start)])
    return out


def _preexisting_answer(question: str) -> str:
    """The scripted answer to the pre-existing question: yes when the Run
    recorded the reported command's failure in a checkout of the target
    branch's commit the context names as the task's base (a call naming
    ../task-base at that commit) with the same FAIL lines as its own run."""
    listed = question.split("### The commit of each target branch this task builds on\n- ", 1)
    sha = listed[1].split("\n", 1)[0].split(" at ")[-1] if len(listed) == 2 else ""
    calls = question.split("### The tool calls this Run recorded, in order", 1)[-1].split("\n- ")[1:]
    base = head = None
    for call in calls:
        fails = sorted({line.strip() for line in call.split("\n") if line.strip().startswith("FAIL: ")})
        if "../task-base" in call:
            if sha and f"../task-base {sha}" in call:
                base = fails
        else:
            head = fails
    return "yes" if base and base == head else "no"


# The chat routing call's built-in instructions open with this (chat.BuiltInInstructions).
CHAT_ROUTING_OPENING = "You route a message someone wrote to dude"
# What else marks a routing call, should the opening change: its answer's shape.
CHAT_ROUTING_SHAPE = '"route":"brainstorm"|"task"|"epic"|"resume"|"clarify"'
# The headings the stub reads the routing input by (chat.RoutingInput).
CHAT_ROUTING_HEADINGS = ("## The message", "## Projects they can file in")


class ChatRouteMismatch(Exception):
    """A routing call the stub cannot read: the opening or a heading it
    reads by changed. The suite fails on it rather than routing blind."""


# The stub runs in run_tests.py's process and the chat suite in pytest's, so
# a mismatch is recorded in a file under the shared log directory, one per line.
_mismatch_lock = threading.Lock()


def record_chat_route_mismatch(path: Path, mismatch: str) -> None:
    line = " ".join(mismatch.split())
    with _mismatch_lock, path.open("a") as f:
        f.write(line + "\n")


def _chat_route(messages: list) -> str | None:
    """The scripted answer to a chat routing call, or None for any other call.

    Deterministic, from the mention's words: "file a task" files one in the
    first project listed, "open an epic" opens one there with three tasks
    (the second and third blocked by the first), anything else is a
    brainstorm in that project. Raises ChatRouteMismatch for a routing call
    whose opening or headings are not the ones it reads.
    """
    system = str(messages[0].get("content", "")) if messages else ""
    if CHAT_ROUTING_OPENING not in system:
        if CHAT_ROUTING_SHAPE in system:
            raise ChatRouteMismatch(f"a routing call without the opening {CHAT_ROUTING_OPENING!r}")
        return None
    user = str(messages[-1].get("content", ""))
    missing = [h for h in CHAT_ROUTING_HEADINGS if h not in user]
    if missing:
        raise ChatRouteMismatch(f"a routing call without the heading(s) {missing}")
    said = user.split("## The message", 1)[-1].split("## ", 1)[0].lower()
    projects = re.findall(r"^- ([A-Z][A-Z0-9]{1,5}) \(", user.split("## Projects they can file in", 1)[-1], re.MULTILINE)
    project = projects[0] if projects else None
    # A channel's "usually about" hint, when routing was given one, is where an
    # unspecific message goes; a message naming a listed project goes there.
    if "## This channel is usually about" in user:
        usual = user.split("## This channel is usually about", 1)[1].strip().split(" (a hint", 1)[0].split(", ")
        project = next((k for k in usual if k in projects), project)
    named = next((k for k in projects if re.search(rf"\b{k.lower()}\b", said)), None)
    if m := re.search(r"this channel is usually about ([a-z0-9 ,]+)", said):
        keys = [k for k in projects if re.search(rf"\b{k.lower()}\b", m[1])]
        return json.dumps({"route": "channel_projects", "confidence": 0.95, "reasoning": "scripted: they say what the channel is about",
                           "channel_projects": {"add": keys, "remove": []}})
    if named:
        project = named
    if "file a task" in said and project:
        return json.dumps({"route": "task", "confidence": 0.95, "reasoning": "scripted: one clear change", "project": project,
                           "title": "Tidy the export page", "goal": "Tidy the export page.", "criteria": ["It is tidy"]})
    if "maybe a task" in said and project:
        # Under the 75% bar: dude asks.
        return json.dumps({"route": "task", "confidence": 0.5, "reasoning": "scripted: unsure", "project": project,
                           "title": "Tidy the export page", "goal": "Tidy the export page."})
    if "open an epic" in said and project:
        tasks = [{"ref": "t1", "title": "Measure export time", "goal": "g", "criteria": [], "blocked_by": []},
                 {"ref": "t2", "title": "Batch the readings query", "goal": "g", "criteria": [], "blocked_by": ["t1"]},
                 {"ref": "t3", "title": "Stream the CSV response", "goal": "g", "criteria": [], "blocked_by": ["t1"]}]
        return json.dumps({"route": "epic", "confidence": 0.9, "reasoning": "scripted: several pieces", "project": project,
                           "new_epic": {"title": "CSV exports", "description": "Make exports fast.", "tasks": tasks}})
    return json.dumps({"route": "brainstorm", "confidence": 0.85, "reasoning": "scripted: a discussion", "project": project,
                       "title": "Chat brainstorm"})


def llm_env() -> dict:
    """DUDE_LLM_URL / DUDE_LLM_KEY from this process, for an orchestrator
    that is to run a real model. Never printed: the key is a secret."""
    return {k: os.environ[k] for k in ("DUDE_LLM_URL", "DUDE_LLM_KEY") if os.environ.get(k)}


def _signal(proc: subprocess.Popen, sig: int, group: bool) -> None:
    """Signal a process, or its whole process group."""
    try:
        if group:
            os.killpg(proc.pid, sig)
        else:
            proc.send_signal(sig)
    except ProcessLookupError:
        pass


def find_free_port() -> int:
    """A free port this process has not handed out before.

    The kernel may hand the same ephemeral port to two binds once the first
    socket is closed, so two callers (an orchestrator and its tools, say)
    could otherwise get the same port before either listens on it.
    """
    with _ports_lock:
        while True:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
                s.bind(("", 0))
                port = int(s.getsockname()[1])
            if port not in _ports_given:
                _ports_given.add(port)
                return port


def workspace_for(run_id: str) -> str:
    """DUDE_TEST_WORKSPACE_ROOT is a parent directory, never a shared workspace."""
    root = os.environ.get("DUDE_TEST_WORKSPACE_ROOT")
    return str(Path(root) / f"e2e-workspace-{run_id}") if root else f"/tmp/dude-e2e-{run_id}"


_ports_given: set[int] = set()
_ports_lock = threading.Lock()


@dataclass
class TestEnvironment:
    run_id: str = field(default_factory=lambda: uuid.uuid4().hex[:8])
    db_name: str = field(init=False)
    control_plane_port: int = field(default_factory=find_free_port)
    orchestrator_port: int = field(default_factory=find_free_port)
    gallery_port: int = field(default_factory=find_free_port)
    web_port: int = field(default_factory=find_free_port)
    web_proc: subprocess.Popen | None = field(default=None, repr=False)
    # A real lux to drive instead of the fake: its env.json (see lux_env).
    real_lux: dict | None = field(default=None, repr=False)
    # Set on the orchestrator over the suite's own settings: a harness
    # measuring a real model passes its LLM's URL and key this way.
    orchestrator_env: dict = field(default_factory=dict, repr=False)
    control_plane_proc: subprocess.Popen | None = field(default=None, repr=False)
    orchestrator_proc: subprocess.Popen | None = field(default=None, repr=False)
    lux_proc: subprocess.Popen | None = field(default=None, repr=False)
    slack_proc: subprocess.Popen | None = field(default=None, repr=False)
    gallery_proc: subprocess.Popen | None = field(default=None, repr=False)
    # Access mode's edge and backend, when a suite started them (start_access).
    _access_procs: list = field(default_factory=list, repr=False)
    # The fake judgement proxy answers only the checks gate's questions:
    # Backed yes unless the context has a recorded error; Missing is no.
    # Every other request gets no answer, as from a proxy nothing answers.
    # It is local and deterministic, never a real model call.
    judge_server: ThreadingHTTPServer | None = field(default=None, repr=False)
    judge_thread: threading.Thread | None = field(default=None, repr=False)
    workspace_root: str = field(init=False)

    def __post_init__(self) -> None:
        self.db_name = f"dude_test_{self.run_id}"
        # Logs from the processes the suite starts. Kept rather than discarded:
        # a failure in the orchestrator is otherwise invisible from the test, which
        # only sees that a Run never reached the state it waited for.
        self.workspace_root = workspace_for(self.run_id)
        self.log_dir = Path(os.environ.get("DUDE_TEST_LOG_DIR", self.workspace_root))
        self.log_dir.mkdir(parents=True, exist_ok=True)
        self.control_plane_url = f"http://localhost:{self.control_plane_port}"
        self.gallery_url = f"http://127.0.0.1:{self.gallery_port}"
        self._init_services()

    def _init_services(self) -> None:
        # Where the fake GitHub's repositories live: git daemon serves this
        # directory, and the fake lux pushes into it.
        self.git_root = Path(self.workspace_root) / "github"
        self.orchestrator_url = f"http://127.0.0.1:{self.orchestrator_port}"
        self.orchestrator_token = f"svc-{self.run_id}"
        self.lux_key = f"lux-{self.run_id}"

    @property
    def chat_route_mismatches_path(self) -> Path:
        return self.log_dir / f"chat-route-mismatches-{self.run_id}.log"

    def chat_route_mismatches(self) -> list[str]:
        """The routing calls the judge stub could not read (ChatRouteMismatch),
        as recorded by whichever process runs the stub."""
        path = self.chat_route_mismatches_path
        return path.read_text().splitlines() if path.exists() else []

    # -- connection strings -------------------------------------------------

    @property
    def owner_dsn(self) -> str:
        return (
            f"postgres://{POSTGRES_OWNER}:{POSTGRES_OWNER_PASSWORD}"
            f"@{POSTGRES_HOST}:{POSTGRES_PORT}/{self.db_name}"
        )

    @property
    def app_dsn(self) -> str:
        return (
            f"postgres://{POSTGRES_APP_USER}:{POSTGRES_APP_PASSWORD}"
            f"@{POSTGRES_HOST}:{POSTGRES_PORT}/{self.db_name}"
        )

    def _admin_dsn(self, dbname: str = POSTGRES_ADMIN_DB) -> str:
        return (
            f"postgres://{POSTGRES_OWNER}:{POSTGRES_OWNER_PASSWORD}"
            f"@{POSTGRES_HOST}:{POSTGRES_PORT}/{dbname}"
        )

    # -- lifecycle ----------------------------------------------------------

    def setup(self) -> None:
        require_bun()
        self._s3_bucket()
        self._create_database()
        self._migrate()
        self.git_root.mkdir(parents=True, exist_ok=True)
        self._start_lux()
        self._start_orchestrator()
        self._start_control_plane()

    def _start_lux(self) -> None:
        """A stand-in for lux: scripted agents, real git pushes.

        With `real_lux`, nothing is started: the orchestrator drives that lux.

        dude's suite tests what dude sends lux and what it does with the
        answers; lux is tested by its own suite, and the contract between
        the two by `--lux`.
        """
        if self.real_lux:
            self.lux_url, self.lux_key = self.real_lux["luxd_url"], self.real_lux["api_key"]
            return
        addr_file = self.log_dir / "fake-lux.addr"
        addr_file.unlink(missing_ok=True)
        self.lux_proc = subprocess.Popen(
            [str(REPO_ROOT / "orchestrator" / "bin" / "fake-lux"), "-root", str(self.git_root),
             "-key", self.lux_key, "-addr-file", str(addr_file),
             # Previews wake on request, as on a lux with previews.
             "-preview-domain", FAKE_PREVIEW_DOMAIN,
             # No host runs nested containers: a Run on an image that can
             # run them waits, as on a pool without one.
             "-no-nested-host",
             # A lux from v0.1.32: preview servers take dude's overlay tag.
             "-server-inject",
             # Each Run's checkout, removed with the rest of the run's files.
             "-workspaces", str(self.git_root.parent),
             # The fake reads a command's /workspace as the Run's checkout;
             # where this host's own checkout is under /workspace too (a lux
             # Run's), the files a test hands a command are still its own.
             "-host-dir", str(self.log_dir), "-host-dir", str(self.workspace_root)],
            stdout=self._log("fake-lux"), stderr=subprocess.STDOUT,
        )
        deadline = time.time() + 10
        while not addr_file.exists() or not addr_file.read_text():
            if time.time() > deadline:
                raise RuntimeError("fake lux did not start")
            time.sleep(0.05)
        self.lux_url = f"http://{addr_file.read_text()}"

    def _start_judge(self) -> None:
        questions = _checks_questions()
        mismatches = self.chat_route_mismatches_path
        # A kept log directory may hold an earlier run's file.
        mismatches.unlink(missing_ok=True)

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers.get("content-length", "0"))
                body = json.loads(self.rfile.read(length) or b"{}")
                messages = body.get("messages", [])
                question = " ".join(str(m.get("content", "")) for m in messages)
                try:
                    routed = _chat_route(messages)
                except ChatRouteMismatch as e:
                    record_chat_route_mismatch(mismatches, str(e))
                    print(f"judge stub: {e}", file=sys.stderr)
                    self.send_response(500)
                    self.send_header("content-length", "0")
                    self.end_headers()
                    return
                if routed is not None:
                    response = json.dumps({"model": "judge-e2e", "choices": [{"message": {"role": "assistant", "content": routed}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 1, "completion_tokens": 1}}).encode()
                    self.send_response(200)
                    self.send_header("content-type", "application/json")
                    self.send_header("content-length", str(len(response)))
                    self.end_headers()
                    self.wfile.write(response)
                    return
                if not any(q in question for q in questions):
                    # Anything but the checks gate's own questions finds no
                    # proxy: the connection closes with no answer, as an
                    # unreachable host's does.
                    self.close_connection = True
                    self.connection.shutdown(socket.SHUT_RDWR)
                    return
                context = question.lower()
                decision = "no" if "missing" in context else "yes"
                if questions[2] in question:
                    decision = _preexisting_answer(question)
                elif "status: error" in context or "fail: testgreeting" in context or "exit status 1" in context:
                    # Judge the latest recorded completion: an earlier
                    # failed turn is not evidence against the corrected turn.
                    last = context.rsplit("status:", 1)[-1]
                    if "error" in last or "exit code 1" in last or "exit status 1" in last:
                        decision = "no"
                answer = json.dumps({"decision": decision, "confidence": 0.99, "reasoning": "scripted e2e judgement"})
                response = json.dumps({"model": "judge-e2e", "choices": [{"message": {"role": "assistant", "content": answer}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 1, "completion_tokens": 1}}).encode()
                self.send_response(200)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(response)))
                self.end_headers()
                self.wfile.write(response)
            def log_message(self, *_args):
                pass
        self.judge_server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.judge_thread = threading.Thread(target=self.judge_server.serve_forever, daemon=True)
        self.judge_thread.start()
        # By name, not 127.0.0.1: a Run's network always lists the model's
        # host, and test_network.py checks that rule by that name.
        self.judge_url = f"http://localhost:{self.judge_server.server_port}/v1"

    def _start_orchestrator(self) -> None:
        self._start_judge()
        self._start_fake_slack()
        self.orchestrator_proc = subprocess.Popen(
            [str(REPO_ROOT / "orchestrator" / "bin" / "dude-orchestrator")],
            env={
                **os.environ,
                "DATABASE_URL": self.app_dsn,
                "DUDE_ORCHESTRATOR_LISTEN": f"127.0.0.1:{self.orchestrator_port}",
                "DUDE_ORCHESTRATOR_TOKEN": self.orchestrator_token,
                "LUX_URL": getattr(self, "lux_url", ""),
                "LUX_API_KEY": self.lux_key,
                # lux-fake's image when the lux is real: it is preloaded on
                # every host, and it is what the fake models run.
                "DUDE_AGENT_IMAGE": "localhost/lux-fake:test" if self.real_lux else "dude-runtime:test",
                "DUDE_LAYER_IMAGE": TEST_LAYER,
                # No real agent credentials in the suite; fake models only,
                # and memory searched by words. The URL is the fake
                # judgement proxy, which answers the checks gate alone.
                "DUDE_LLM_URL": self.judge_url,
                "DUDE_LLM_KEY": "",
                "DUDE_EMBEDDINGS_URL": "off",
                # Only the contract gateway (or an explicit local topology smoke)
                # may use separate HTTP API and git-daemon ports.
                "DUDE_TEST_GITHUB_GIT_HOST": (
                    self.real_lux["gateway"] if self.real_lux
                    else os.environ.get("DUDE_TEST_GITHUB_GIT_HOST", "")
                ),
                # An agent waiting on a person is parked after seconds, not
                # the policy's minutes, so the suite sees it happen.
                "DUDE_PARK_AFTER": "3s",
                # A task's conductor is parked a few seconds after its turn.
                "DUDE_CONDUCTOR_WARM": "4s",
                # A working agent's diff is read every few seconds besides
                # after each edit, so a test sees the slow path too.
                "DUDE_DIFF_EVERY": "3s",
                # Commenting on previews is on (with the fake lux's -server-inject). Its preview
                # pages are the browser suite's own (test_preview_feedback.py), naming the web
                # app's real origin in the tag: this origin only has to be an https one.
                "DUDE_AUTH_PUBLIC_URL": FAKE_PUBLIC_URL,
                # The images people send agents: it reads what the backend stored.
                **self.s3_env,
                **self._tools_env(),
                # Slack is the fake's (fake-slack): its Web API and OAuth pages.
                **self.slack_env,
                **self.orchestrator_env,
            },
            stdout=self._log("orchestrator"), stderr=subprocess.STDOUT,
        )

    def _start_fake_slack(self) -> None:
        """A stand-in for Slack (orchestrator/cmd/fake-slack): the Web API calls
        and OAuth pages dude uses, and /_fake/ controls a test drives it with.
        The orchestrator calls it as DUDE_SLACK_API_URL; a test signs Events API
        payloads with SLACK_SIGNING_SECRET, as Slack signs them."""
        addr_file = self.log_dir / "fake-slack.addr"
        addr_file.unlink(missing_ok=True)
        self.slack_proc = subprocess.Popen(
            [str(REPO_ROOT / "orchestrator" / "bin" / "fake-slack"), "-client-id", SLACK_CLIENT_ID,
             "-client-secret", SLACK_CLIENT_SECRET, "-addr-file", str(addr_file)],
            stdout=self._log("fake-slack"), stderr=subprocess.STDOUT,
        )
        deadline = time.time() + 10
        while not addr_file.exists() or not addr_file.read_text():
            if time.time() > deadline:
                raise RuntimeError("fake slack did not start")
            time.sleep(0.05)

    @property
    def fake_slack_url(self) -> str:
        """The fake Slack's address, as it wrote it on start."""
        return f"http://{(self.log_dir / 'fake-slack.addr').read_text()}"

    @property
    def slack_env(self) -> dict:
        return {"DUDE_SLACK_SIGNING_SECRET": SLACK_SIGNING_SECRET, "DUDE_SLACK_CLIENT_ID": SLACK_CLIENT_ID,
                "DUDE_SLACK_CLIENT_SECRET": SLACK_CLIENT_SECRET, "DUDE_SLACK_API_URL": f"{self.fake_slack_url}/api"}

    def _tools_env(self) -> dict:
        """dude's tools for agents: always with the fake lux; with a real one
        when its hosts can reach us.

        lux never lets a Run reach its own host or lux's address, so the
        tools listen on an address of this machine that lux's hosts can
        reach but is neither: DUDE_TEST_TOOLS_HOST (a LAN address).
        """
        if not self.real_lux:
            # The fake lux calls them from this machine, as its proxy would.
            port = find_free_port()
            return {"DUDE_TOOLS_LISTEN": f"127.0.0.1:{port}", "DUDE_TOOLS_URL": f"http://127.0.0.1:{port}/"}
        host = os.environ.get("DUDE_TEST_TOOLS_HOST")
        if not host:
            return {}
        port = find_free_port()
        return {"DUDE_TOOLS_LISTEN": f"{host}:{port}", "DUDE_TOOLS_URL": f"http://{host}:{port}/"}

    def _create_database(self) -> None:
        with psycopg.connect(self._admin_dsn(), autocommit=True) as conn:
            conn.execute(f'CREATE DATABASE "{self.db_name}" OWNER {POSTGRES_OWNER}')

    def _migrate(self) -> None:
        """Run migrations as the owner, exactly as a deployment would."""
        result = subprocess.run(
            ["bun", "run", "apps/control-plane/src/db/migrate.ts"],
            cwd=REPO_ROOT,
            env={**os.environ, "DATABASE_URL": self.owner_dsn},
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            raise RuntimeError(f"migrations failed:\n{result.stdout}\n{result.stderr}")

    def _start_control_plane(self) -> None:
        self.control_plane_proc = subprocess.Popen(
            ["bun", "run", "apps/control-plane/src/index.ts"],
            cwd=REPO_ROOT,
            env={
                **os.environ,
                # Traffic runs as the restricted role so tenant isolation is
                # exercised by every test, not just the ones that test it.
                "DATABASE_URL": self.app_dsn,
                "PORT": str(self.control_plane_port),
                "DUDE_ORCHESTRATOR_URL": self.orchestrator_url,
                "DUDE_ORCHESTRATOR_TOKEN": self.orchestrator_token,
                "DUDE_LAYER_IMAGE": TEST_LAYER,
                **self.s3_env,
            },
            stdout=self._log("control-plane"),
            stderr=subprocess.STDOUT,
        )

    def start_gallery(self) -> str:
        """Serve the built design-system gallery.

        Serves the build output rather than running the Vite dev server: the UI
        tests should exercise what ships, and a dev server adds HMR sockets and
        on-demand transforms that make failures ambiguous. Uses the stdlib
        server so the suite needs no extra tooling.
        """
        dist = REPO_ROOT / "packages" / "design-system" / "dist" / "gallery"
        if not dist.exists():
            raise RuntimeError(
                f"gallery not built at {dist} — run `bun run gallery:build` in packages/design-system"
            )

        self.gallery_proc = subprocess.Popen(
            [sys.executable, "-m", "http.server", str(self.gallery_port), "--bind", "127.0.0.1"],
            cwd=dist,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        return self.gallery_url

    def start_web(self) -> str:
        """Serve the built web app, proxying the API to this run's control plane.

        `vite preview` rather than the dev server, for the same reason as the
        gallery: the tests should exercise what ships. On 127.0.0.1, where
        `web_url` looks for it: vite's own default host is localhost, which
        on macOS resolves to ::1 first.
        """
        # On 127.0.0.1, where web_url looks: vite's default, localhost, is
        # only ::1 on macOS, so the app came up where nothing looked.
        self.web_proc = subprocess.Popen(
            ["bunx", "vite", "preview", "--host", "127.0.0.1"],
            cwd=REPO_ROOT / "apps" / "web",
            env={
                **os.environ,
                "DUDE_CONTROL_PLANE": self.control_plane_url,
                "DUDE_WEB_PORT": str(self.web_port),
            },
            stdout=self._log("web"),
            stderr=subprocess.STDOUT,
            # bunx runs vite as a child; its own group lets teardown stop both.
            start_new_session=True,
        )
        deadline = time.time() + 30
        while time.time() < deadline:
            try:
                if requests.get(self.web_url, timeout=1).ok:
                    return self.web_url
            except requests.RequestException:
                pass
            time.sleep(0.3)
        raise RuntimeError(f"web app did not come up on {self.web_url}")

    @property
    def web_url(self) -> str:
        return f"http://127.0.0.1:{self.web_port}"

    def start_access(self, super_admins: list[str]) -> "AccessMode":
        """The backend as Cloudflare Access fronts it, beside the API-key one.

        A local stand-in for the Access team (tests/access-edge.ts) publishes
        its keys and signs tokens for any email the suite names; a second
        backend (tests/access-server.ts) runs with `DUDE_AUTH_PROVIDER=
        cloudflare_access` against this run's database and orchestrator,
        verifies every request's CF_Authorization token against those keys,
        and serves the built web app on its own origin. Its super-admins are
        `super_admins`. Stopped with the environment.
        """
        designation = uuid.uuid4().hex
        addr_file = self.log_dir / f"access-edge-{designation[:8]}.addr"
        edge = subprocess.Popen(
            ["bun", "run", "tests/access-edge.ts"], cwd=REPO_ROOT,
            env={**os.environ, "ACCESS_EDGE_DESIGNATION": designation, "ACCESS_EDGE_ADDR_FILE": str(addr_file), "PORT": "0"},
            stdout=self._log("access-edge"), stderr=subprocess.STDOUT,
        )
        self._access_procs.append(edge)
        deadline = time.time() + 30
        while not addr_file.exists() or not addr_file.read_text():
            if edge.poll() is not None or time.time() > deadline:
                raise RuntimeError("the Access edge did not start; see access-edge.log")
            time.sleep(0.1)
        edge_url = f"http://{addr_file.read_text()}"
        port = find_free_port()
        backend = subprocess.Popen(
            ["bun", "run", "tests/access-server.ts"], cwd=REPO_ROOT,
            env={
                **os.environ,
                "DATABASE_URL": self.app_dsn,
                "PORT": str(port),
                "DUDE_ORCHESTRATOR_URL": self.orchestrator_url,
                "DUDE_ORCHESTRATOR_TOKEN": self.orchestrator_token,
                "DUDE_LAYER_IMAGE": TEST_LAYER,
                "DUDE_WEB_DIR": str(REPO_ROOT / "apps" / "web" / "dist"),
                "DUDE_AUTH_PROVIDER": "cloudflare_access",
                # Replaced by the server's own origin (access-server.ts); configuration requires https.
                "DUDE_AUTH_PUBLIC_URL": "https://dude.access.test",
                "DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM": ACCESS_TEAM,
                "DUDE_AUTH_CLOUDFLARE_ACCESS_AUD": ACCESS_AUD,
                "DUDE_AUTH_SUPER_ADMINS": ",".join(super_admins),
                "ACCESS_EDGE_URL": edge_url,
                **self.s3_env,
            },
            stdout=self._log("access-backend"), stderr=subprocess.STDOUT,
        )
        self._access_procs.append(backend)
        url = f"http://127.0.0.1:{port}"
        deadline = time.time() + 30
        while True:
            if backend.poll() is not None:
                raise RuntimeError(f"the Access backend exited ({backend.returncode}); see access-backend.log")
            try:
                if requests.get(f"{url}/health", timeout=1).ok:
                    break
            except requests.RequestException:
                pass
            if time.time() > deadline:
                raise RuntimeError("the Access backend did not come up; see access-backend.log")
            time.sleep(0.2)
        return AccessMode(url=url, edge_url=edge_url, designation=designation)

    def stop_access(self) -> None:
        for proc in reversed(self._access_procs):
            _signal(proc, signal.SIGTERM, False)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                _signal(proc, signal.SIGKILL, False)
        self._access_procs.clear()

    @property
    def fake_lux_url(self) -> str:
        """The fake lux's address, as it wrote it on start: a test reads what it was sent (GET /v1/runs/<id>)."""
        return f"http://{(self.log_dir / 'fake-lux.addr').read_text()}"

    def _log(self, name: str):
        """An append-mode log file for one of the suite's processes."""
        return open(self.log_dir / f"{name}.log", "ab")

    def wait_healthy(self, timeout: float = 30.0) -> bool:
        """Both the backend and the orchestrator answer /health."""
        deadline = time.time() + timeout
        for proc, url in ((self.control_plane_proc, self.control_plane_url),
                          (self.orchestrator_proc, self.orchestrator_url)):
            while True:
                if proc and proc.poll() is not None:
                    return False  # exited; no point waiting out the timeout
                try:
                    resp = requests.get(f"{url}/health", timeout=1)
                    if resp.status_code == 200 and resp.json().get("status") == "ok":
                        break
                except requests.RequestException:
                    pass
                if time.time() > deadline:
                    return False
                time.sleep(0.1)
        return True

    def teardown(self, keep: bool = False) -> None:
        self.stop_access()
        for name in ("web_proc", "gallery_proc", "control_plane_proc", "orchestrator_proc", "lux_proc", "slack_proc"):
            proc = getattr(self, name, None)
            if proc is None:
                continue
            # The web app is `bunx vite`: vite is bunx's child and would outlive
            # it, holding the port with a proxy to a backend that is gone.
            group = name == "web_proc"
            _signal(proc, signal.SIGTERM, group)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                _signal(proc, signal.SIGKILL, group)
            setattr(self, name, None)


        if keep:
            return

        self._drop_database()
        self._drop_s3_bucket()
        subprocess.run(["rm", "-rf", self.workspace_root], check=False)

    # -- object storage -----------------------------------------------------

    @property
    def s3_bucket(self) -> str:
        return f"dude-test-{self.run_id}"

    @property
    def s3_env(self) -> dict:
        return {
            "DUDE_S3_BUCKET": self.s3_bucket,
            "DUDE_S3_ENDPOINT": f"http://127.0.0.1:{S3_PORT}",
            "DUDE_S3_REGION": S3_REGION,
            "DUDE_S3_ACCESS_KEY": S3_ACCESS_KEY,
            "DUDE_S3_SECRET_KEY": S3_SECRET_KEY,
        }

    def s3(self):
        return boto3.client("s3", endpoint_url=f"http://127.0.0.1:{S3_PORT}", aws_access_key_id=S3_ACCESS_KEY,
                            aws_secret_access_key=S3_SECRET_KEY, region_name=S3_REGION)

    def _s3_bucket(self) -> None:
        # Runs set up side by side: one starts the container, under a lock,
        # and the others find it running.
        with open(f"/tmp/{S3_CONTAINER}.lock", "w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            running = subprocess.run(["docker", "inspect", "-f", "{{.State.Running}}", S3_CONTAINER],
                                     capture_output=True, text=True).stdout.strip() == "true"
            if not running:
                subprocess.run(["docker", "rm", "-f", S3_CONTAINER], capture_output=True)
                subprocess.run(
                    ["docker", "run", "-d", "--name", S3_CONTAINER, "--label", "dude-e2e",
                     "-p", f"127.0.0.1:{S3_PORT}:9000",
                     "-e", f"ROOT_ACCESS_KEY={S3_ACCESS_KEY}", "-e", f"ROOT_SECRET_KEY={S3_SECRET_KEY}",
                     S3_IMAGE, "--port", ":9000", "--health", "/health", "--region", S3_REGION, "posix", "/tmp"],
                    check=True, capture_output=True,
                )
        deadline = time.time() + 30
        while True:
            try:
                if requests.get(f"http://127.0.0.1:{S3_PORT}/health", timeout=2).ok:
                    break
            except requests.RequestException:
                pass
            if time.time() > deadline:
                raise RuntimeError(f"the test S3 ({S3_CONTAINER}) did not come up on port {S3_PORT}")
            time.sleep(0.3)
        self.s3().create_bucket(Bucket=self.s3_bucket)

    def _drop_s3_bucket(self) -> None:
        try:
            s3 = self.s3()
            for page in s3.get_paginator("list_objects_v2").paginate(Bucket=self.s3_bucket):
                for obj in page.get("Contents", []):
                    s3.delete_object(Bucket=self.s3_bucket, Key=obj["Key"])
            s3.delete_bucket(Bucket=self.s3_bucket)
        except Exception as err:  # noqa: BLE001 - teardown is best effort
            print(f"warning: could not remove bucket {self.s3_bucket}: {err}", file=sys.stderr)

    def _drop_database(self) -> None:
        try:
            with psycopg.connect(self._admin_dsn(), autocommit=True) as conn:
                # Terminate stragglers first, or DROP DATABASE blocks.
                conn.execute(
                    "SELECT pg_terminate_backend(pid) FROM pg_stat_activity "
                    "WHERE datname = %s AND pid <> pg_backend_pid()",
                    (self.db_name,),
                )
                conn.execute(f'DROP DATABASE IF EXISTS "{self.db_name}"')
        except Exception as exc:  # noqa: BLE001 - cleanup must not mask failures
            print(f"warning: failed to drop {self.db_name}: {exc}")

    @classmethod
    def from_env(cls) -> TestEnvironment:
        """Reconstruct in the pytest subprocess, which run_tests.py spawns."""
        env = cls.__new__(cls)
        env.run_id = os.environ["DUDE_TEST_RUN_ID"]
        env.db_name = f"dude_test_{env.run_id}"
        env.control_plane_port = int(os.environ["DUDE_TEST_CONTROL_PLANE_PORT"])
        env.gallery_port = int(os.environ.get("DUDE_TEST_GALLERY_PORT", "0"))
        env.web_port = int(os.environ.get("DUDE_TEST_WEB_PORT", "0"))
        env.web_proc = None
        env.control_plane_url = f"http://localhost:{env.control_plane_port}"
        env.gallery_url = f"http://127.0.0.1:{env.gallery_port}"
        env.workspace_root = workspace_for(env.run_id)
        # The parent's log directory, so anything started here logs beside
        # the control plane rather than somewhere nobody will look.
        env.log_dir = Path(os.environ.get("DUDE_TEST_LOG_DIR", env.workspace_root))
        env.log_dir.mkdir(parents=True, exist_ok=True)
        env.control_plane_proc = None
        env.orchestrator_proc = None
        env.lux_proc = None
        env.slack_proc = None
        env.gallery_proc = None
        env._access_procs = []
        env.orchestrator_port = int(os.environ.get("DUDE_TEST_ORCHESTRATOR_PORT", "0"))
        env.real_lux = lux_env() if os.environ.get("DUDE_TEST_REAL_LUX") else None
        env.orchestrator_env = {}
        env._init_services()
        # The lux the orchestrator drives: the fake's address, or the real one's.
        env.lux_url = os.environ.get("DUDE_TEST_LUX_URL", "")
        if env.real_lux:
            env.lux_url, env.lux_key = env.real_lux["luxd_url"], env.real_lux["api_key"]
        return env
