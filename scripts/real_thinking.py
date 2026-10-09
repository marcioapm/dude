# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Real OpenCode, real proxy, real thinking.

Runs `opencode run --format json --thinking` on a small read-only question in
this repository, once per model at an effort, with OPENCODE_CONFIG_CONTENT
exactly as buildSpec makes it (TestPrintATiersOpenCodeConfig in
orchestrator/internal/phases) layered over images/runtime/opencode.json as
the image does (OPENCODE_CONFIG), and prints each run's reasoning parts.
`--thinking` is what makes `opencode run` print reasoning parts at all.

Not a CI test: it needs the proxy's key.

    DUDE_LLM_KEY=... uv run scripts/real_thinking.py [EFFORT] [MODEL ...]

DUDE_LLM_URL defaults to https://llmproxy.absmartly-dev.com/v1. The key is
never printed (and is cut from any stderr shown). The user's own OpenCode
config, auth and data are kept out: XDG_* point at a scratch directory, so
only the image's providers exist. Exits non-zero unless every model, within
TRIES tries, gives a reasoning part with text.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TRIES = 3
DEFAULT_URL = "https://llmproxy.absmartly-dev.com/v1"
# A question that needs thought on the agent's own turn: adaptive thinking
# skips a trivial one, and gpt-6-sol sends a reasoning item with no summary
# when it has little to reason about.
QUESTION = (
    "Read migrations/096_tier_effort.sql. Reason it through step by step before answering: "
    "which of these headers objects does model_tier_headers_valid accept, and why each? "
    '{"X-Team":"a"}, {"X Team":"a"}, {"a":1}, {"a":"b\\nc"}, {"~ok":""}. Change nothing.'
)
PREFIX = "OPENCODE_CONFIG_CONTENT="


def tier_config(model: str, effort: str) -> str:
    """The config buildSpec gives a phase Run on a tier with this model and effort."""
    env = {**os.environ, "DUDE_PRINT_TIER_CONFIG": f"{model} {effort}"}
    out = subprocess.run(
        ["go", "-C", str(ROOT / "orchestrator"), "test", "-count=1", "-v",
         "-run", "^TestPrintATiersOpenCodeConfig$", "./internal/phases"],
        env=env, capture_output=True, text=True, check=True,
    ).stdout
    for line in out.splitlines():
        if line.startswith(PREFIX):
            return line[len(PREFIX):]
    sys.exit(f"TestPrintATiersOpenCodeConfig printed no {PREFIX}:\n{out}")


def report(events: str, key: str) -> bool:
    """Prints the run's events, errors, reasoning and answer; True when a reasoning part has text."""
    kinds: dict[str, int] = {}
    reasoning, text, errors = [], [], []
    for line in events.splitlines():
        line = line.strip()
        if not line.startswith("{"):
            continue
        ev = json.loads(line)
        kinds[ev.get("type")] = kinds.get(ev.get("type"), 0) + 1
        part = ev.get("part") or {}
        if part.get("type") == "reasoning":
            reasoning.append(part.get("text", ""))
        if part.get("type") == "text":
            text.append(part.get("text", ""))
        if ev.get("type") == "error":
            errors.append(json.dumps(ev.get("error"))[:400])
    print("events:", kinds)
    for e in errors:
        print("error:", e.replace(key, "<key>"))
    nonempty = [r for r in reasoning if r.strip()]
    print(f"reasoning parts: {len(reasoning)}, non-empty: {len(nonempty)}")
    for r in nonempty[:3]:
        print("  reasoning:", r.strip().replace("\n", " ")[:400])
    print("answer:", " ".join(t.strip() for t in text)[:400])
    return bool(nonempty)


def attempt(model: str, config: str, scratch: Path, key: str) -> bool:
    home = Path(tempfile.mkdtemp(prefix=model.replace("/", "_") + "-", dir=scratch))
    xdg = {}
    for name in ("config", "data", "cache", "state"):
        (home / name).mkdir()
        xdg[f"XDG_{name.upper()}_HOME"] = str(home / name)
    env = {**os.environ, **xdg,
           "OPENCODE_CONFIG": str(ROOT / "images/runtime/opencode.json"),
           "OPENCODE_CONFIG_CONTENT": config}
    run = subprocess.run(
        ["opencode", "run", "--format", "json", "--thinking", "--dir", str(ROOT), QUESTION],
        env=env, capture_output=True, text=True,
    )
    if report(run.stdout, key):
        return True
    print(f"exit status: {run.returncode}")
    print("stderr:")
    print(run.stderr.replace(key, "<key>").rstrip() or "(empty)")
    return False


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("effort", nargs="?", default="high", help="the tier's effort (default high)")
    parser.add_argument("models", nargs="*", default=["claude-sonnet-5", "gpt-6-sol"],
                        help="models as the proxy names them (default claude-sonnet-5 gpt-6-sol)")
    args = parser.parse_args()
    # Each try's report before the next one starts, and before stderr's verdict.
    sys.stdout.reconfigure(line_buffering=True)
    key = os.environ.get("DUDE_LLM_KEY")
    if not key:
        parser.error("set DUDE_LLM_KEY to the key of the proxy")
    os.environ.setdefault("DUDE_LLM_URL", DEFAULT_URL)

    failed = []
    with tempfile.TemporaryDirectory() as scratch:
        for model in args.models:
            config = tier_config(model, args.effort)
            print(f"=== {model} at {args.effort}")
            print(PREFIX + config)
            # A model decides per turn whether to summarise its reasoning
            # (gpt-6-sol sometimes sends a reasoning item with no summary on a
            # tool-using turn): a few tries, each shown.
            for n in range(1, TRIES + 1):
                print(f"--- try {n}")
                if attempt(model, config, Path(scratch), key):
                    break
            else:
                print(f"{model}: no non-empty reasoning part in {TRIES} tries", file=sys.stderr)
                failed.append(model)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
