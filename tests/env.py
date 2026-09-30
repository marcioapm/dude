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
import os
import socket
import sys
import signal
import subprocess
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path

import boto3
import psycopg
import requests


REPO_ROOT = Path(__file__).resolve().parent.parent

# Photos go to object storage. The suite shares one S3 gateway over a
# local directory, started on first use and left running like the dev
# Postgres; each run makes and removes its own bucket.
S3_IMAGE = os.environ.get("DUDE_TEST_S3_IMAGE", "versity/versitygw:v1.7.0")
S3_CONTAINER = "dude-e2e-s3"
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

# The backend stores photos in versitygw, which closes the connection after
# each PUT; Bun before this reports that as ConnectionClosed and fails the
# upload (apps/control-plane/src/storage.ts, MIN_BUN_FOR_S3).
MIN_BUN = (1, 4, 0)


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
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("", 0))
        return int(s.getsockname()[1])


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
    control_plane_proc: subprocess.Popen | None = field(default=None, repr=False)
    orchestrator_proc: subprocess.Popen | None = field(default=None, repr=False)
    lux_proc: subprocess.Popen | None = field(default=None, repr=False)
    gallery_proc: subprocess.Popen | None = field(default=None, repr=False)
    workspace_root: str = field(init=False)

    def __post_init__(self) -> None:
        self.db_name = f"dude_test_{self.run_id}"
        # Logs from the processes the suite starts. Kept rather than discarded:
        # a failure in the orchestrator is otherwise invisible from the test, which
        # only sees that a Run never reached the state it waited for.
        self.log_dir = Path(os.environ.get("DUDE_TEST_LOG_DIR", f"/tmp/dude-e2e-{self.run_id}"))
        self.log_dir.mkdir(parents=True, exist_ok=True)
        self.control_plane_url = f"http://localhost:{self.control_plane_port}"
        self.gallery_url = f"http://127.0.0.1:{self.gallery_port}"
        self.workspace_root = f"/tmp/dude-e2e-{self.run_id}"
        self._init_services()

    def _init_services(self) -> None:
        # Where the fake GitHub's repositories live: git daemon serves this
        # directory, and the fake lux pushes into it.
        self.git_root = Path(self.workspace_root) / "github"
        self.orchestrator_url = f"http://127.0.0.1:{self.orchestrator_port}"
        self.orchestrator_token = f"svc-{self.run_id}"
        self.lux_key = f"lux-{self.run_id}"

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
             # Each Run's checkout, removed with the rest of the run's files.
             "-workspaces", str(self.git_root.parent)],
            stdout=self._log("fake-lux"), stderr=subprocess.STDOUT,
        )
        deadline = time.time() + 10
        while not addr_file.exists() or not addr_file.read_text():
            if time.time() > deadline:
                raise RuntimeError("fake lux did not start")
            time.sleep(0.05)
        self.lux_url = f"http://{addr_file.read_text()}"

    def _start_orchestrator(self) -> None:
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
                # No real agent credentials in the suite; fake models only,
                # and memory searched by words.
                "DUDE_LLM_URL": "",
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
                # A working agent's diff is read every few seconds besides
                # after each edit, so a test sees the slow path too.
                "DUDE_DIFF_EVERY": "3s",
                **self._tools_env(),
            },
            stdout=self._log("orchestrator"), stderr=subprocess.STDOUT,
        )

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
        gallery: the tests should exercise what ships.
        """
        self.web_proc = subprocess.Popen(
            ["bunx", "vite", "preview"],
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
        for name in ("web_proc", "gallery_proc", "control_plane_proc", "orchestrator_proc", "lux_proc"):
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
        env.workspace_root = f"/tmp/dude-e2e-{env.run_id}"
        # The parent's log directory, so anything started here logs beside
        # the control plane rather than somewhere nobody will look.
        env.log_dir = Path(os.environ.get("DUDE_TEST_LOG_DIR", env.workspace_root))
        env.log_dir.mkdir(parents=True, exist_ok=True)
        env.control_plane_proc = None
        env.orchestrator_proc = None
        env.lux_proc = None
        env.gallery_proc = None
        env.orchestrator_port = int(os.environ.get("DUDE_TEST_ORCHESTRATOR_PORT", "0"))
        env.real_lux = lux_env() if os.environ.get("DUDE_TEST_REAL_LUX") else None
        env._init_services()
        return env
