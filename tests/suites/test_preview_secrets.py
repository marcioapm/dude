"""Preview secrets, through the public API against the deployed backend,
orchestrator and fake lux.

A project's secret reaches a branch preview's servers as an environment
variable, and a replaced value reaches it at its next wake. No agent Run
gets it. The fake lux gives a server's process the environment lux's shim
would (the Run's env secrets under the server's own env), and runs a
server command marked `fakelux-run` through sh, so a server here writes
what `printenv` sees to a file this test reads.
"""

from __future__ import annotations

import os
from pathlib import Path

import requests

from helpers import ApiClient, wait_until

NAME = "SEED_LLM_KEY"


def _probe(directory: Path, label: str) -> tuple[Path, str]:
    """A file a server writes the secret into, and the shell line that writes it."""
    out = directory / f"{label}.env"
    return out, f"printenv {NAME} > {out}; echo exit=$? >> {out} # fakelux-run"


def _written(path: Path, what: str) -> str:
    return wait_until(lambda: path.exists() and "exit=" in (t := path.read_text()) and t, timeout=60,
                      message=f"{what} never wrote what it saw")


def test_a_preview_server_sees_the_project_secret_and_a_replaced_value_at_its_next_wake(
        client: ApiClient, forge_project: dict, env):
    pid = forge_project["id"]
    probes = env.log_dir / f"secrets-{os.urandom(4).hex()}"
    probes.mkdir()
    value = f"sk-test-{os.urandom(6).hex()}\nsecond line"
    added = client.post(f"/v1/projects/{pid}/secrets", {"name": NAME, "value": value})
    assert added.status_code == 201, added.text
    assert added.json()["hint"] == "line" and value not in added.text

    first, line = _probe(probes, "first")
    recipe = {"name": "web", "port": 3000, "command": line, "workdir": "", "env": [{"name": "PORT", "value": "3000"}],
              "autostartInPreviews": True}
    assert client.put(f"/v1/projects/{pid}/servers/web", recipe).status_code == 200

    task = client.create_task(pid, "Preview with a secret")
    assert client.post(f"/v1/tasks/{task['id']}/preview").status_code == 201
    asleep = wait_until(lambda: (s := client.get(f"/v1/tasks/{task['id']}/servers").json())["run"].get("asleep") and s,
                        timeout=60, message="the preview never went to sleep")
    web = asleep["servers"][0]
    lux_fake(env, f"/fake/servers/{web['id']}/request?path=/")
    assert _written(first, "the preview's server") == f"{value}\nexit=0\n"
    lux_run = client.get(f"/v1/tasks/{task['id']}/servers").json()["run"]["luxRunId"]
    secrets = lux_get(env, f"/v1/runs/{lux_run}")["spec"]["secrets"]
    assert {"name": NAME, "as": "env"} in [{k: s[k] for k in ("name", "as") if k in s} for s in secrets], secrets

    # Replaced while it runs; asleep, then woken: the same Run, the new value.
    new_value = f"sk-live-{os.urandom(6).hex()}"
    assert client.put(f"/v1/projects/{pid}/secrets/{NAME}", {"value": new_value}).status_code == 200
    assert lux_fake(env, f"/fake/servers/{web['id']}/idle")["idle"] is True
    wait_until(lambda: lux_get(env, f"/v1/runs/{lux_run}")["state"] == "stopped", timeout=60,
               message="the idle preview's Run was never stopped")
    first.unlink()
    lux_fake(env, f"/fake/servers/{web['id']}/request?path=/")
    assert _written(first, "the woken preview's server") == f"{new_value}\nexit=0\n"
    assert client.get(f"/v1/tasks/{task['id']}/servers").json()["run"]["luxRunId"] == lux_run


def test_an_agent_session_does_not_get_the_project_secret(client: ApiClient, forge_project: dict, env):
    pid = forge_project["id"]
    value = f"sk-test-{os.urandom(6).hex()}"
    assert client.post(f"/v1/projects/{pid}/secrets", {"name": NAME, "value": value}).status_code == 201
    client.patch(f"/v1/projects/{pid}", {"agentModels": client.on_models({"implementer": "fake/hang"})})
    task = client.create_task(pid, "An agent with a secret about")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code in (200, 201, 202)
    run = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["status"] == "running"), None),
                     timeout=60, message="the implementer never started")

    # What lux was asked to run declares no such secret.
    lux_run = client.get(f"/v1/runs/{run['id']}/servers").json()["run"]["luxRunId"]
    spec = lux_get(env, f"/v1/runs/{lux_run}")["spec"]
    assert NAME not in [s["name"] for s in spec.get("secrets", [])], spec.get("secrets")

    # A process in the agent's Run sees the Run's environment, as lux's shim
    # gives it: no SEED_LLM_KEY there.
    probes = env.log_dir / f"secrets-{os.urandom(4).hex()}"
    probes.mkdir()
    out, line = _probe(probes, "agent")
    added = client.post(f"/v1/runs/{run['id']}/servers", {"name": "probe", "port": 4100, "command": line})
    assert added.status_code == 201, added.text
    assert _written(out, "the agent Run's server") == "exit=1\n"

    assert client.post(f"/v1/runs/{run['id']}/abort", {"reason": "done"}).status_code == 200


def lux_get(env, path: str) -> dict:
    res = requests.get(env.lux_url + path, headers={"Authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    return res.json()


def lux_fake(env, path: str) -> dict:
    res = requests.post(env.lux_url + path, headers={"Authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    return res.json()
