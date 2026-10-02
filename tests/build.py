"""Build the artifacts the E2E suite drives.

The Bun backend runs from source; the orchestrator and the fake lux are Go
and are built every time — a stale binary tests yesterday's code.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent


GALLERY_DIST = REPO_ROOT / "packages" / "design-system" / "dist" / "gallery"
# What the gallery is built from: the design system and the domain package it
# imports. A build older than any of these is stale.
GALLERY_SOURCES = (
    REPO_ROOT / "packages" / "design-system" / "src",
    REPO_ROOT / "packages" / "design-system" / "vite.config.ts",
    REPO_ROOT / "packages" / "design-system" / "package.json",
    REPO_ROOT / "packages" / "domain" / "src",
)


def _newest(path: Path) -> float:
    if path.is_file():
        return path.stat().st_mtime
    return max((p.stat().st_mtime for p in path.rglob("*") if p.is_file()), default=0.0)


def gallery_is_stale() -> bool:
    """No build yet, or a source changed since it was made."""
    built = GALLERY_DIST / "index.html"
    if not built.exists():
        return True
    return max(_newest(s) for s in GALLERY_SOURCES if s.exists()) > built.stat().st_mtime


def build_gallery(force: bool = False) -> Path:
    """Build the design-system gallery that the UI tests drive.

    Reused only while it is newer than everything it is built from: a gallery
    built before a section was added fails that section's tests for no reason
    in the code.
    """
    if not force and not gallery_is_stale():
        return GALLERY_DIST

    print("building design-system gallery...")
    result = subprocess.run(
        ["bun", "run", "gallery:build"],
        cwd=REPO_ROOT / "packages" / "design-system",
    )
    if result.returncode != 0:
        print("gallery build failed", file=sys.stderr)
        sys.exit(1)
    return GALLERY_DIST


WEB_DIST = REPO_ROOT / "apps" / "web" / "dist"


def build_web(force: bool = False) -> Path:
    """Build the web app the UI tests drive.

    Always rebuilt unless skipped by the caller: unlike the gallery, the app
    changes with almost every piece of work, and a test against a stale build
    passes or fails for reasons that have nothing to do with the code.
    """
    print("building web app...")
    result = subprocess.run(["bun", "run", "build"], cwd=REPO_ROOT / "apps" / "web",
                            stdout=subprocess.DEVNULL)
    if result.returncode != 0:
        print("web build failed", file=sys.stderr)
        sys.exit(1)
    return WEB_DIST


def build() -> None:
    """Build the orchestrator and the fake lux into orchestrator/bin."""
    print("building orchestrator...")
    result = subprocess.run(["go", "build", "-o", "bin/", "./cmd/..."], cwd=REPO_ROOT / "orchestrator")
    if result.returncode != 0:
        print("go build failed", file=sys.stderr)
        sys.exit(1)
