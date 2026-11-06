#!/usr/bin/env python3
"""Run isolated E2E stacks; --plan collects without building or starting services."""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import tempfile
import time
from pathlib import Path

sys.path.insert(0, os.path.dirname(__file__))

from build import build, build_gallery, build_web
from env import TestEnvironment, llm_env, lux_env, require_bun
from e2e_receipt import check_receipts, write
from shard import TESTS_DIR, names_a_suite, parse_shard


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--build", action="store_true", help="force gallery rebuild")
    parser.add_argument("--keep", action="store_true", help="keep environment for debugging")
    parser.add_argument("--no-ui", action="store_true")
    parser.add_argument("--lux", action="store_true")
    parser.add_argument("--shard", default="", metavar="N/M")
    parser.add_argument("--plan", action="store_true", help="collect only, without builds/services")
    parser.add_argument("--receipt", type=Path, help="write JSON plan and execution receipt")
    args, pytest_args = parser.parse_known_args(argv)
    spec = args.shard or "1/1"
    parse_shard(spec)
    if args.shard and any(names_a_suite(a) for a in pytest_args):
        parser.error("--shard picks its own suites: name none")
    if not any(names_a_suite(a) for a in pytest_args):
        pytest_args.insert(0, str(TESTS_DIR / "suites"))
    marker = "(lux or both_luxes)" if args.lux else "not lux"
    if args.no_ui:
        marker += " and not ui"
    # Combine user markers rather than letting our default silently overwrite them.
    for index in range(len(pytest_args) - 1, -1, -1):
        arg = pytest_args[index]
        if arg.startswith(("-m=", "--markexpr=")):
            marker = f"({arg.split('=', 1)[1]}) and ({marker})"
            del pytest_args[index]
        elif arg in ("-m", "--markexpr"):
            marker = f"({pytest_args[index + 1]}) and ({marker})"
            del pytest_args[index:index + 2]
    pytest_args += ["-m", marker]
    with tempfile.TemporaryDirectory(prefix="dude-e2e-plan-") as temporary:
        receipt = args.receipt.resolve() if args.receipt else Path(temporary) / "receipt.json"
        canonical = Path(temporary) / "plan.json"
        state = {"complete": False, "pytest_complete": False, "exit_code": 1, "results": [], "phases": {}}
        write(receipt, state)  # invalidate a receipt left by any earlier invocation
        process_env = {**os.environ, "DUDE_E2E_RECEIPT": str(canonical),
                       "DUDE_E2E_PLAN": "1", "DUDE_E2E_SHARD": spec}
        command = [sys.executable, "-m", "pytest", "-p", "e2e_receipt", *pytest_args]
        started = time.monotonic()
        code = subprocess.run([*command, "--collect-only", "-q"], cwd=TESTS_DIR, env=process_env).returncode
        state["phases"]["collection"] = time.monotonic() - started
        if canonical.exists():
            state.update(json.loads(canonical.read_text()))
        state.update(complete=False, pytest_complete=False, exit_code=code)
        write(receipt, state)
        if code or args.plan:
            return code
        process_env.update(DUDE_E2E_PLAN="0", DUDE_E2E_RECEIPT=str(receipt),
                           DUDE_E2E_CANONICAL_PLAN=str(canonical))
        env = None
        code = 1
        phase = "build"
        started = time.monotonic()
        try:
            require_bun()
            build()
            if not args.no_ui:
                build_gallery(force=args.build)
                build_web()
            state["phases"][phase] = time.monotonic() - started
            phase, started = "environment", time.monotonic()
            env = TestEnvironment(real_lux=lux_env() if args.lux else None,
                                  orchestrator_env=llm_env() if args.lux else {})
            print(f"run id: {env.run_id}; database: {env.db_name}; logs: {env.log_dir}", flush=True)
            env.setup()
            if not env.wait_healthy(timeout=30):
                raise RuntimeError("control plane did not become healthy")
            for key, value in {
                "RUN_ID": env.run_id, "CONTROL_PLANE_PORT": env.control_plane_port,
                "ORCHESTRATOR_PORT": env.orchestrator_port, "LUX_URL": env.lux_url,
                "GALLERY_PORT": env.gallery_port, "WEB_PORT": env.web_port, "LOG_DIR": env.log_dir,
            }.items():
                process_env[f"DUDE_TEST_{key}"] = str(value)
            if args.lux:
                process_env["DUDE_TEST_REAL_LUX"] = "1"
            state["phases"][phase] = time.monotonic() - started
            phase, started = "pytest", time.monotonic()
            code = subprocess.run(command, cwd=TESTS_DIR, env=process_env).returncode
            executed = json.loads(receipt.read_text())
            state.update({k: v for k, v in executed.items() if k != "phases"})
            if code == 0:
                validate_local(state)
        except Exception as error:
            print(f"E2E {phase} failed: {error}", file=sys.stderr)
            code = code or 1
        finally:
            state["phases"][phase] = time.monotonic() - started
            started = time.monotonic()
            teardown_ok = False
            try:
                if env is not None:
                    env.teardown(keep=args.keep)
                teardown_ok = True
            finally:
                state["phases"]["teardown"] = time.monotonic() - started
                state.update(exit_code=code, complete=code == 0 and teardown_ok)
                if not teardown_ok:
                    state.update(exit_code=code or 1, complete=False)
                write(receipt, state)
                print("E2E phases (seconds): " + json.dumps(state["phases"]), flush=True)
        return code


def validate_local(state):
    from collections import Counter
    if (not state["pytest_complete"] or state["exit_code"] != 0
            or Counter(r["nodeid"] for r in state["results"]) != Counter(state["selected"])):
        raise ValueError("unfinished or incomplete pytest receipt")
    if any(r["outcome"] not in ("passed", "skipped") for r in state["results"]):
        raise ValueError("failed terminal result")


if __name__ == "__main__":
    sys.exit(main())
