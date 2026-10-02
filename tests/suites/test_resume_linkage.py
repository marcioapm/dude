"""helpers.assert_timed_is_its_row: a run.resume.timed's numbers are its
row's, rounded exactly as the orchestrator rounds them (Go's math.Round,
half away from zero), negative ones included.

Pure checks with fixed rows; they need no environment:

    uv run --directory tests pytest suites/test_resume_linkage.py
"""
from datetime import datetime, timedelta, timezone

import pytest

from helpers import assert_timed_is_its_row, whole_ms

T0 = datetime(2026, 10, 2, 9, 0, 0, tzinfo=timezone.utc)


@pytest.mark.parametrize("us,ms", [
    (499, 0), (500, 1), (501, 1), (1499, 1), (1500, 2),
    (-499, 0), (-500, -1), (-501, -1), (-1500, -2),
    (0, 0), (86_400_000_500, 86_400_001),
])
def test_whole_ms_rounds_half_away_from_zero(us, ms):
    assert whole_ms(T0, T0 + timedelta(microseconds=us)) == ms


def row(first_output_us: int, reload_us: int = 2000) -> dict:
    """A resume whose stamps are a millisecond apart, but for its reload
    (across lux's clock and dude's) and its first output."""
    at = lambda us: T0 + timedelta(microseconds=us)  # noqa: E731
    return {"epoch": 2, "cause": "person", "moved": False, "host_name": "h",
            "woken_at": at(0), "requested_at": at(1000), "assigned_at": at(2000), "image_ready_at": at(3000),
            "volumes_restored_at": at(4000), "workload_started_at": at(5000), "running_at": at(5000 + reload_us),
            "busy_at": at(8000), "first_output_at": at(8000 + first_output_us)}


def payload(first_output: int, reload: int = 2) -> dict:
    return {"epoch": 2, "cause": "person", "moved": False, "hostName": "h",
            "totalMs": 8 + first_output, "untilBusyMs": 8,
            "phases": {"react": 1, "schedule": 1, "image": 1, "restore": 1, "start": 1, "reload": reload,
                       "take": 3 - reload, "firstOutput": first_output}}


@pytest.mark.parametrize("us,right,wrong", [(499, 0, 1), (500, 1, 0), (501, 1, 0)])
def test_a_phase_is_its_rounded_row_and_nothing_else(us, right, wrong):
    assert_timed_is_its_row(payload(right), row(us))
    with pytest.raises(AssertionError):
        assert_timed_is_its_row(payload(wrong), row(us))


@pytest.mark.parametrize("us,right,wrong", [(-499, 0, -1), (-500, -1, 0), (-501, -1, 0)])
def test_a_negative_cross_clock_phase_is_allowed_and_rounded_the_same(us, right, wrong):
    p = payload(1, reload=right)
    assert_timed_is_its_row(p, row(1000, reload_us=us))
    with pytest.raises(AssertionError):
        assert_timed_is_its_row(payload(1, reload=wrong), row(1000, reload_us=us))


def test_a_fractional_millisecond_is_not_what_the_orchestrator_writes():
    p = payload(1)
    p["phases"]["firstOutput"] = 0.6
    with pytest.raises(AssertionError):
        assert_timed_is_its_row(p, row(600))
