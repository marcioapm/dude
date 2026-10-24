"""The judge stub's routing-call mismatches reach the pytest process."""

import json
import subprocess
import sys
from pathlib import Path

import requests

from env import CHAT_ROUTING_HEADINGS, CHAT_ROUTING_OPENING, TestEnvironment as Environment

TESTS_DIR = Path(__file__).resolve().parent.parent

READ_IN_CHILD = (
    "import json; from env import TestEnvironment; "
    "print(json.dumps(TestEnvironment.from_env().chat_route_mismatches()))"
)


def _route(url: str, user: str) -> requests.Response:
    messages = [{"role": "system", "content": CHAT_ROUTING_OPENING + "."}, {"role": "user", "content": user}]
    return requests.post(f"{url}/chat/completions", json={"messages": messages}, timeout=5)


def _child_reads(monkeypatch, parent: Environment) -> list[str]:
    monkeypatch.setenv("DUDE_TEST_RUN_ID", parent.run_id)
    monkeypatch.setenv("DUDE_TEST_CONTROL_PLANE_PORT", str(parent.control_plane_port))
    monkeypatch.delenv("DUDE_TEST_REAL_LUX", raising=False)
    out = subprocess.run([sys.executable, "-c", READ_IN_CHILD], cwd=TESTS_DIR, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def test_a_routing_call_the_stub_cannot_read_is_seen_by_the_pytest_process(monkeypatch, tmp_path):
    monkeypatch.setenv("DUDE_TEST_WORKSPACE_ROOT", str(tmp_path))
    monkeypatch.setenv("DUDE_TEST_LOG_DIR", str(tmp_path / "logs"))
    parent = Environment(run_id="judge-stub")
    parent._start_judge()
    try:
        readable = "\n".join(f"{h}\nx\n" for h in CHAT_ROUTING_HEADINGS)
        assert _route(parent.judge_url, readable).status_code == 200
        assert _child_reads(monkeypatch, parent) == []

        assert _route(parent.judge_url, "no headings here").status_code == 500
        [mismatch] = _child_reads(monkeypatch, parent)
        assert all(h in mismatch for h in CHAT_ROUTING_HEADINGS)
    finally:
        parent.judge_server.shutdown()


def test_a_new_stub_starts_from_no_mismatches(monkeypatch, tmp_path):
    monkeypatch.setenv("DUDE_TEST_WORKSPACE_ROOT", str(tmp_path))
    monkeypatch.setenv("DUDE_TEST_LOG_DIR", str(tmp_path / "logs"))
    parent = Environment(run_id="judge-stub")
    parent.chat_route_mismatches_path.write_text("an earlier run's mismatch\n")
    parent._start_judge()
    parent.judge_server.shutdown()
    assert parent.chat_route_mismatches() == []
