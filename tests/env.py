"""TestEnvironment — isolated per-run environment for the E2E suite.

Each run gets its own PostgreSQL database and free ports, so suites can run
concurrently and a failed run leaves no residue in a shared database.

The suite drives the factory through its public HTTP API only. It never
imports the Bun implementation, which is what makes it a real test of the
deployed system rather than of internal functions (plan §29, §50).
"""

from __future__ import annotations

import os
import socket
import sys
import subprocess
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path

import psycopg
import requests

REPO_ROOT = Path(__file__).resolve().parent.parent

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


def find_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("", 0))
        return int(s.getsockname()[1])


@dataclass
class TestEnvironment:
    run_id: str = field(default_factory=lambda: uuid.uuid4().hex[:8])
    db_name: str = field(init=False)
    control_plane_port: int = field(default_factory=find_free_port)
    gallery_port: int = field(default_factory=find_free_port)
    control_plane_proc: subprocess.Popen | None = field(default=None, repr=False)
    runner_proc: subprocess.Popen | None = field(default=None, repr=False)
    gallery_proc: subprocess.Popen | None = field(default=None, repr=False)
    workspace_root: str = field(init=False)

    def __post_init__(self) -> None:
        self.db_name = f"dude_test_{self.run_id}"
        self.control_plane_url = f"http://localhost:{self.control_plane_port}"
        self.gallery_url = f"http://127.0.0.1:{self.gallery_port}"
        self.workspace_root = f"/tmp/dude-e2e-{self.run_id}"

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
        self._create_database()
        self._migrate()
        self._start_control_plane()

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
            },
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
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

    def start_runner(self, api_key: str, max_runs: int = 2) -> None:
        """Start the Go runner. Only needed by suites that execute Runs."""
        binary = REPO_ROOT / "runner" / "bin" / "factory-runner"
        if not binary.exists():
            raise RuntimeError(f"runner binary not built: {binary}")

        self.runner_proc = subprocess.Popen(
            [
                str(binary),
                "--workspace-root", self.workspace_root,
                "--max-runs", str(max_runs),
                "--name", f"e2e-runner-{self.run_id}",
                "--poll-interval", "500ms",
            ],
            env={
                **os.environ,
                "DUDE_CONTROL_PLANE": self.control_plane_url,
                "DUDE_RUNNER_KEY": api_key,
            },
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )

    def wait_healthy(self, timeout: float = 30.0) -> bool:
        deadline = time.time() + timeout
        while time.time() < deadline:
            if self.control_plane_proc and self.control_plane_proc.poll() is not None:
                return False  # exited; no point waiting out the timeout
            try:
                resp = requests.get(f"{self.control_plane_url}/health", timeout=1)
                if resp.status_code == 200 and resp.json().get("status") == "ok":
                    return True
            except requests.RequestException:
                pass
            time.sleep(0.1)
        return False

    def teardown(self, keep: bool = False) -> None:
        for name in ("runner_proc", "gallery_proc", "control_plane_proc"):
            proc = getattr(self, name, None)
            if proc is None:
                continue
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
            setattr(self, name, None)

        self._remove_containers()

        if keep:
            return

        self._drop_database()
        subprocess.run(["rm", "-rf", self.workspace_root], check=False)

    def _remove_containers(self) -> None:
        """Remove Run containers this environment created.

        A leaked container holds CPU and disk after the test that made it has
        gone, so cleanup is unconditional rather than best-effort.
        """
        result = subprocess.run(
            ["docker", "ps", "-aq", "--filter", "label=dude.managed=true"],
            capture_output=True,
            text=True,
            check=False,
        )
        ids = [line for line in result.stdout.split() if line]
        if ids:
            subprocess.run(["docker", "rm", "-f", *ids], capture_output=True, check=False)

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
        env.control_plane_url = f"http://localhost:{env.control_plane_port}"
        env.gallery_url = f"http://127.0.0.1:{env.gallery_port}"
        env.workspace_root = f"/tmp/dude-e2e-{env.run_id}"
        env.control_plane_proc = None
        env.runner_proc = None
        env.gallery_proc = None
        return env
