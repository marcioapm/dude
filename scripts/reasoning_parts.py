"""Reads `opencode run --format json` events and prints their reasoning parts.

Exits non-zero when no reasoning part has text. Used by real-thinking.sh.
"""

import json
import sys

path, model = sys.argv[1], sys.argv[2]
kinds, reasoning, text, errors = {}, [], [], []
for line in open(path):
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
    print("error:", e)
nonempty = [r for r in reasoning if r.strip()]
print(f"reasoning parts: {len(reasoning)}, non-empty: {len(nonempty)}")
for r in nonempty[:3]:
    print("  reasoning:", r.strip().replace("\n", " ")[:400])
print("answer:", " ".join(t.strip() for t in text)[:400])
sys.exit(0 if nonempty else f"{model}: no non-empty reasoning part")
