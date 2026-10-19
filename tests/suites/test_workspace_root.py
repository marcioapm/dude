"""Workspace selection is identical in the runner and pytest subprocess."""

from env import TestEnvironment as Environment, workspace_for


def test_workspace_override_keeps_runs_isolated(monkeypatch, tmp_path):
    monkeypatch.setenv("DUDE_TEST_WORKSPACE_ROOT", str(tmp_path))
    monkeypatch.setenv("DUDE_TEST_LOG_DIR", str(tmp_path / "logs"))
    parent = Environment(run_id="workspace-one")
    monkeypatch.setenv("DUDE_TEST_RUN_ID", parent.run_id)
    monkeypatch.setenv("DUDE_TEST_CONTROL_PLANE_PORT", str(parent.control_plane_port))
    monkeypatch.delenv("DUDE_TEST_REAL_LUX", raising=False)
    child = Environment.from_env()
    assert parent.workspace_root == str(tmp_path / "e2e-workspace-workspace-one")
    assert child.workspace_root == parent.workspace_root
    assert workspace_for("workspace-two") != parent.workspace_root


def test_workspace_default_is_unchanged(monkeypatch):
    monkeypatch.delenv("DUDE_TEST_WORKSPACE_ROOT", raising=False)
    assert workspace_for("workspace-default") == "/tmp/dude-e2e-workspace-default"
