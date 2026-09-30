"""The suite's Bun preflight: which `bun --version` outputs may run it.

Pure checks of env.bun_problem / env.require_bun; they need no environment.
"""
import json
import subprocess
from pathlib import Path

import pytest

import env

FLOOR = ".".join(map(str, env.MIN_BUN))
REASON = "earlier Bun fails every photo upload to the test S3 (versitygw answers Connection: close)"


@pytest.mark.parametrize("version", ["1.4.0", "1.4.2\n", "  1.4.2  ", "1.4.0+build", "1.4.1-canary.3+abc", "1.10.0", "2.0.0"])
def test_a_bun_at_or_above_the_floor_may_run_the_suite(version):
    assert env.bun_problem(version) is None


@pytest.mark.parametrize("version", ["1.3.9", "1.3.14", "0.9.0", "1.4.0-canary.1", "1.4.0-canary.1+abc"])
def test_an_older_bun_is_refused_with_the_floor_and_the_reason(version):
    assert env.bun_problem(version) == f"Bun {version} is on PATH; the suite needs Bun >= {FLOOR}: {REASON}"


@pytest.mark.parametrize("version", ["", "1.4", "v1.4.0", "1.4.x", "latest"])
def test_output_that_is_not_a_version_is_refused_rather_than_guessed_at(version):
    assert env.bun_problem(version) == (
        f"could not read the Bun version from {version.strip()!r}; the suite needs Bun >= {FLOOR}"
    )


def test_the_floor_is_the_one_package_json_asks_for():
    engines = json.loads((Path(__file__).parents[2] / "package.json").read_text())["engines"]["bun"]
    assert engines == f">={FLOOR}"


def _bun_answers(monkeypatch, result):
    def run(cmd, **kwargs):
        assert cmd == ["bun", "--version"]
        if isinstance(result, BaseException):
            raise result
        return subprocess.CompletedProcess(cmd, 0, stdout=result, stderr="")

    monkeypatch.setattr(env.subprocess, "run", run)


def test_require_bun_passes_a_supported_bun(monkeypatch):
    _bun_answers(monkeypatch, "1.4.2\n")
    env.require_bun()


def test_require_bun_stops_on_an_older_bun(monkeypatch):
    _bun_answers(monkeypatch, "1.3.9\n")
    with pytest.raises(SystemExit) as stop:
        env.require_bun()
    assert stop.value.code == f"Bun 1.3.9 is on PATH; the suite needs Bun >= {FLOOR}: {REASON}"


@pytest.mark.parametrize("error", [FileNotFoundError("bun"), subprocess.CalledProcessError(1, ["bun", "--version"])])
def test_require_bun_stops_when_bun_cannot_be_asked(monkeypatch, error):
    _bun_answers(monkeypatch, error)
    with pytest.raises(SystemExit) as stop:
        env.require_bun()
    assert str(stop.value.code).startswith("bun --version failed: ")
