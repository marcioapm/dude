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


def build_gallery(force: bool = False) -> Path:
    """Build the design-system gallery that the UI tests drive."""
    if GALLERY_DIST.exists() and not force:
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
