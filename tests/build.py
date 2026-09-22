"""Build the artifacts the E2E suite drives.

The Bun control plane runs from source, so only the Go runner needs building.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
RUNNER_BINARY = REPO_ROOT / "runner" / "bin" / "factory-runner"


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


def build(force: bool = False) -> Path:
    """Build the runner binary. Returns its path."""
    if RUNNER_BINARY.exists() and not force:
        return RUNNER_BINARY

    print("building factory-runner...")
    result = subprocess.run(
        ["go", "build", "-o", "bin/factory-runner", "./cmd/factory-runner"],
        cwd=REPO_ROOT / "runner",
    )
    if result.returncode != 0:
        print("go build failed", file=sys.stderr)
        sys.exit(1)

    if not RUNNER_BINARY.exists():
        print(f"runner binary missing at {RUNNER_BINARY}", file=sys.stderr)
        sys.exit(1)

    print(f"runner ready: {RUNNER_BINARY}")
    return RUNNER_BINARY
