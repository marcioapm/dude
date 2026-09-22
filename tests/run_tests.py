#!/usr/bin/env python3
"""Entry point for the dude E2E test suite.

Usage:
    python run_tests.py                          # run all suites
    python run_tests.py suites/test_events.py    # run one suite
    python run_tests.py -x                       # stop on first failure
    python run_tests.py -k "tenant"              # filter by name
    python run_tests.py --build                  # force rebuild the runner
    python run_tests.py --keep                   # keep the environment (debug)
    python run_tests.py -v                       # verbose

Each invocation creates an isolated PostgreSQL database and starts the
control plane on a free port, so runs do not collide with each other or with
a development instance.
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, os.path.dirname(__file__))

from build import build, build_gallery  # noqa: E402
from env import TestEnvironment  # noqa: E402

TESTS_DIR = Path(__file__).resolve().parent


def main() -> None:
    parser = argparse.ArgumentParser(description="Run dude E2E tests")
    parser.add_argument("--build", action="store_true", help="force rebuild the runner binary")
    parser.add_argument("--keep", action="store_true", help="keep the test environment after the run")
    parser.add_argument("--no-runner", action="store_true", help="skip suites that need Docker")
    parser.add_argument("--no-ui", action="store_true", help="skip suites that drive a browser")
    args, pytest_args = parser.parse_known_args()

    build(force=args.build)
    if not args.no_ui:
        build_gallery(force=args.build)

    env = TestEnvironment()
    print(f"run id:        {env.run_id}")
    print(f"database:      {env.db_name}")
    print(f"control plane: {env.control_plane_url}")

    exit_code = 1
    try:
        env.setup()
        if not env.wait_healthy(timeout=30):
            print("ERROR: control plane did not become healthy", file=sys.stderr)
            env.teardown(keep=args.keep)
            sys.exit(2)
        print("control plane healthy, running tests\n")

        # The pytest subprocess reconstructs the environment from these.
        os.environ["DUDE_TEST_RUN_ID"] = env.run_id
        os.environ["DUDE_TEST_CONTROL_PLANE_PORT"] = str(env.control_plane_port)
        os.environ["DUDE_TEST_GALLERY_PORT"] = str(env.gallery_port)

        skip_marks = []
        if args.no_runner:
            skip_marks.append("not docker")
        if args.no_ui:
            skip_marks.append("not ui")
        if skip_marks:
            pytest_args += ["-m", " and ".join(skip_marks)]

        if not pytest_args or all(a.startswith("-") for a in pytest_args):
            pytest_args = [str(TESTS_DIR / "suites"), *pytest_args]

        result = subprocess.run(
            [sys.executable, "-m", "pytest", *pytest_args],
            env={**os.environ},
            cwd=TESTS_DIR,
        )
        exit_code = result.returncode
    finally:
        env.teardown(keep=args.keep)
        if args.keep:
            print(f"\nenvironment kept — database: {env.db_name}, workspaces: {env.workspace_root}")

    sys.exit(exit_code)


if __name__ == "__main__":
    main()
