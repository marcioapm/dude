#!/usr/bin/env python3
"""Entry point for the dude E2E test suite.

Usage:
    python run_tests.py                          # run all suites
    python run_tests.py suites/test_events.py    # run one suite
    python run_tests.py -x                       # stop on first failure
    python run_tests.py -k "tenant"              # filter by name
    python run_tests.py --build                  # force rebuild the gallery
    python run_tests.py --keep                   # keep the environment (debug)
    python run_tests.py -v                       # verbose
    python run_tests.py --lux                    # the contract suite, against a real lux
    python run_tests.py --shard 2/6              # the 2nd of 6 shares of the suites, as CI runs them

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

from build import build, build_gallery, build_web  # noqa: E402
from env import TestEnvironment, llm_env, lux_env, require_bun  # noqa: E402
from shard import all_suites, parse_shard, split, suite_seconds  # noqa: E402

TESTS_DIR = Path(__file__).resolve().parent


def main() -> None:
    parser = argparse.ArgumentParser(description="Run dude E2E tests")
    parser.add_argument("--build", action="store_true", help="force rebuild the gallery")
    parser.add_argument("--keep", action="store_true", help="keep the test environment after the run")
    parser.add_argument("--no-ui", action="store_true", help="skip suites that drive a browser")
    parser.add_argument("--lux", action="store_true",
                        help="run the contract suite against a real lux instead (see suites/test_lux_contract.py)")
    parser.add_argument("--shard", default="", metavar="N/M",
                        help="run only the Nth of M shares of the suites, split by recorded duration (shard.py)")
    args, pytest_args = parser.parse_known_args()

    if args.shard:
        if any((TESTS_DIR / a.split("::")[0]).exists() for a in pytest_args if not a.startswith("-")):
            sys.exit("--shard picks its own suites: name none")
        n, m = parse_shard(args.shard)
        selected = split(all_suites(), m, suite_seconds())[n - 1]
        if not selected:
            # Without paths pytest would run every suite, not none.
            print(f"shard {n}/{m}: no suites")
            sys.exit(0)
        print(f"shard {n}/{m}: {' '.join(Path(s).name for s in selected)}")
        pytest_args = [*selected, *pytest_args]

    require_bun()
    build()
    if not args.no_ui:
        build_gallery(force=args.build)
        build_web()

    # A real lux may run a real model (the opt-in tests that need
    # DUDE_LLM_KEY): its URL and key go to the orchestrator, never printed.
    env = TestEnvironment(real_lux=lux_env() if args.lux else None, orchestrator_env=llm_env() if args.lux else {})
    print(f"run id:        {env.run_id}")
    print(f"database:      {env.db_name}")
    print(f"control plane: {env.control_plane_url}")
    print(f"orchestrator:  {env.orchestrator_url}")
    print(f"logs:          {env.log_dir}")

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
        os.environ["DUDE_TEST_ORCHESTRATOR_PORT"] = str(env.orchestrator_port)
        os.environ["DUDE_TEST_LUX_URL"] = env.lux_url
        if args.lux:
            os.environ["DUDE_TEST_REAL_LUX"] = "1"
        os.environ["DUDE_TEST_GALLERY_PORT"] = str(env.gallery_port)
        os.environ["DUDE_TEST_WEB_PORT"] = str(env.web_port)
        # So anything the pytest process starts logs beside the rest,
        # rather than into a directory named for a run id the subprocess
        # would otherwise mint for itself.
        os.environ["DUDE_TEST_LOG_DIR"] = str(env.log_dir)

        # One orchestrator drives one lux, so the contract suite gets an
        # environment of its own rather than sharing the fake's. Tests marked
        # both_luxes run in either: they pin the fake to the real one.
        skip_marks = ["(lux or both_luxes)"] if args.lux else ["not lux"]
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
