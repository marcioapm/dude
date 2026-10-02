"""tests/build.py's gallery freshness: when a built gallery is reused and
when it is rebuilt. Pure checks on a temporary tree with set timestamps;
they need no environment and build nothing:

    uv run --directory tests pytest suites/test_gallery_build.py
"""
import os
from pathlib import Path

import pytest

import build

BUILT = 1_000_000.0  # when the gallery was built
OLDER = BUILT - 100
NEWER = BUILT + 100


def at(path: Path, when: float) -> Path:
    os.utime(path, (when, when))
    return path


@pytest.fixture
def tree(tmp_path: Path):
    """Sources all older than a built gallery: fresh."""
    src = tmp_path / "src"
    (src / "components").mkdir(parents=True)
    for f in (src / "components" / "Button.module.css", src / "index.ts"):
        f.write_text("x")
        at(f, OLDER)
    at(src / "components", OLDER)
    at(src, OLDER)
    lock = tmp_path / "bun.lock"
    lock.write_text("x")
    at(lock, OLDER)
    dist = tmp_path / "dist"
    dist.mkdir()
    (dist / "index.html").write_text("x")
    at(dist / "index.html", BUILT)
    return src, lock, dist


def stale(tree) -> bool:
    src, lock, dist = tree
    return build.gallery_is_stale(dist, (src, lock, src.parent / "absent.json"))


def test_a_build_newer_than_every_source_is_reused(tree):
    assert not stale(tree)


def test_no_build_is_stale(tree):
    (tree[2] / "index.html").unlink()
    assert stale(tree)


def test_a_changed_source_file_makes_it_stale(tree):
    at(tree[0] / "components" / "Button.module.css", NEWER)
    assert stale(tree)


def test_a_changed_lockfile_makes_it_stale(tree):
    at(tree[1], NEWER)
    assert stale(tree)


def test_a_deleted_nested_source_makes_it_stale(tree):
    components = tree[0] / "components"
    (components / "Button.module.css").unlink()
    at(components, NEWER)
    assert stale(tree)


def test_deleting_the_last_file_at_the_source_root_makes_it_stale(tree):
    src = tree[0]
    (src / "index.ts").unlink()
    at(src, NEWER)
    assert stale(tree)


def test_every_listed_input_exists():
    """A renamed input would silently drop out of the check."""
    missing = [str(p.relative_to(build.REPO_ROOT)) for p in build.GALLERY_SOURCES if not p.exists()]
    assert missing == []
