# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Real Claude Code and Codex, real proxy, through dude's translator.

For each harness, builds the Run's command, env and secrets exactly as
buildSpec does (TestPrintAHarnessSpec in orchestrator/internal/phases), adds
what lux's adapter adds (Claude Code: -p and stream-json, --mcp-config;
Codex: app-server and its MCP server), and runs the real CLI on a small
question in this repository that makes it plan, read, grep, run a command,
write and edit a scratch file under /tmp, and call dude's emit_event tool on
a stand-in MCP server this script serves. It plays lux's side of each
protocol and writes what lux would send dude, one {"type", "data"} record a
line (claude.* and codex.*), to orchestrator/internal/phases/testdata, with
the key cut out. Then it feeds that file to the translator
(TestPrintATranslation) and prints the dude events it made.

Not a CI test: it needs the proxy's key.

    DUDE_LLM_KEY=... uv run scripts/real_harnesses.py [claude-code|codex ...]

DUDE_LLM_URL defaults to https://llmproxy.absmartly-dev.com/v1;
DUDE_TEST_PG (for the translator's database) to 127.0.0.1:55951. The
user's own Claude Code and Codex configuration are kept out: HOME is a
scratch directory. Exits non-zero unless each harness's translation has an
agent.thought with text, an agent.message and an agent.tool.called.
"""

import argparse
import http.server
import json
import os
import subprocess
import sys
import tempfile
import threading
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TESTDATA = ROOT / "orchestrator/internal/phases/testdata"
DEFAULT_URL = "https://llmproxy.absmartly-dev.com/v1"
MODELS = {"claude-code": ("claude-sonnet-5", "high"), "codex": ("gpt-6-sol", "high")}
SCRATCH = "/tmp/hx-scratch/notes.txt"
QUESTION = (
    "Work through these steps in order; this is a test of your tools. "
    "1. Make a todo list of the steps with your todo/plan tool. "
    "2. Read the first 20 lines of README.md with your file-reading tool. "
    "3. Search docs/operations.md for the word 'harness' with your Grep tool if you have one, else rg. "
    "4. Find files matching scripts/*.py with your Glob tool if you have one, else rg --files. "
    "5. Run `git log --oneline -1` in the shell. "
    f"6. Create the file {SCRATCH} containing the line 'first' with your file-writing or patch tool, not the shell. "
    f"7. Edit {SCRATCH} so the line reads 'second' with your edit or patch tool, not the shell. "
    "8. Call the dude MCP tool emit_event with type 'progress' and data {\"done\": 1, \"of\": 1}. "
    "Then read migrations/099_tier_effort.sql and reason it through step by step before answering: which of these "
    "headers objects does model_tier_headers_valid accept, and why each? "
    '{"X-Team":"a"}, {"X Team":"a"}, {"a":1}, {"a":"b\\nc"}. '
    "Finally say in one sentence what dude is, from README.md. "
    "Change nothing in this repository."
)
TRIES = 5


class MCP(http.server.BaseHTTPRequestHandler):
    """dude's MCP server, as far as an agent sees it: emit_event and list_tasks."""

    calls: list = []

    def log_message(self, *a):
        pass

    def do_GET(self):
        self.send_response(405)
        self.end_headers()

    def do_DELETE(self):
        self.send_response(200)
        self.end_headers()

    def do_POST(self):
        msg = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        if "id" not in msg:
            self.send_response(202)
            self.end_headers()
            return
        method, params = msg.get("method"), msg.get("params") or {}
        if method == "initialize":
            result = {"protocolVersion": params.get("protocolVersion", "2025-06-18"),
                      "capabilities": {"tools": {}}, "serverInfo": {"name": "dude", "version": "1"}}
        elif method == "tools/list":
            result = {"tools": [
                {"name": "emit_event", "description": "Record an event on your run for the people following it.",
                 "inputSchema": {"type": "object", "properties": {"type": {"type": "string"}, "data": {"type": "object"}},
                                 "required": ["type"]}},
                {"name": "list_tasks", "description": "List the project's tasks.",
                 "inputSchema": {"type": "object", "properties": {}}},
            ]}
        elif method == "tools/call":
            MCP.calls.append(params)
            result = {"content": [{"type": "text", "text": json.dumps({"recorded": params.get("arguments", {})})}]}
        else:
            result = {}
        body = json.dumps({"jsonrpc": "2.0", "id": msg["id"], "result": result}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def serve_mcp() -> str:
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), MCP)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return f"http://127.0.0.1:{srv.server_address[1]}/mcp"


def harness_spec(harness: str, url: str) -> dict:
    model, effort = MODELS[harness]
    env = {**os.environ, "DUDE_PRINT_HARNESS_SPEC": f"{harness} {model} {effort}", "DUDE_LLM_URL": url}
    out = subprocess.run(
        ["go", "-C", str(ROOT / "orchestrator"), "test", "-count=1", "-v", "-run", "^TestPrintAHarnessSpec$", "./internal/phases"],
        env=env, capture_output=True, text=True, check=True).stdout
    for line in out.splitlines():
        if line.startswith("HARNESS_SPEC="):
            return json.loads(line[len("HARNESS_SPEC="):])
    sys.exit(f"no HARNESS_SPEC printed:\n{out}")


def run_env(spec: dict, key: str, home: str) -> dict:
    """The spec's env and env secrets; its file secrets written under home, as lux places them."""
    env = {k: v for k, v in os.environ.items() if not k.startswith(("ANTHROPIC_", "OPENAI_", "CLAUDE_", "CODEX_"))}
    env.update(spec["env"])
    for name, sec in spec["secrets"].items():
        value = sec["value"].replace("<key>", key)
        if sec["as"] == "file":
            path = Path(home) / Path(sec["path"]).relative_to("/home/agent")
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(value)
        else:
            env[name] = value
    env["HOME"] = home
    return env


def claude(spec: dict, key: str, home: str, mcp: str) -> list:
    """Claude Code as lux's claude adapter runs it; its lines as lux records them."""
    cfg = Path(home) / "mcp.json"
    cfg.write_text(json.dumps({"mcpServers": {"dude": {"type": "http", "url": mcp, "headers": {}}}}))
    argv = spec["command"] + ["-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
                              "--mcp-config", str(cfg)]
    p = subprocess.Popen(argv, cwd=ROOT, env=run_env(spec, key, home), stdin=subprocess.PIPE,
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    p.stdin.write(json.dumps({"type": "user", "message": {"role": "user", "content": [{"type": "text", "text": QUESTION}]},
                              "parent_tool_use_id": None, "uuid": "00000000-0000-5000-8000-000000000001"}) + "\n")
    p.stdin.flush()
    records = []
    for line in p.stdout:
        line = line.strip()
        if not line.startswith("{"):
            continue
        m = json.loads(line)
        if m.get("type") == "result":
            # lux relays a result line as claude.turn_end (its usage) first,
            # then the line itself (lux internal/adapter/claude.go).
            turn_end = {}
            if m.get("usage") is not None:
                turn_end["usage"] = m["usage"]
            records.append({"type": "claude.turn_end", "data": turn_end})
            records.append({"type": "claude.result", "data": m})
            break
        records.append({"type": "claude." + m.get("type", ""), "data": m})
    p.stdin.close()
    try:
        p.wait(timeout=30)
    except subprocess.TimeoutExpired:
        p.kill()
    if p.returncode not in (0, None, -9):
        print("claude stderr:", p.stderr.read()[-2000:].replace(key, "<key>"))
    return records


def codex(spec: dict, key: str, home: str, mcp: str) -> list:
    """Codex's app-server as lux's codex adapter drives it; its notifications as lux records them."""
    argv = spec["command"] + ["app-server", "-c", "mcp_servers.dude.url=" + json.dumps(mcp)]
    p = subprocess.Popen(argv, cwd=ROOT, env=run_env(spec, key, home), stdin=subprocess.PIPE,
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    next_id = [0]

    def call(method, params):
        next_id[0] += 1
        p.stdin.write(json.dumps({"id": next_id[0], "method": method, "params": params}) + "\n")
        p.stdin.flush()
        return next_id[0]

    records, usage, waiting = [], None, {}
    waiting[call("initialize", {"clientInfo": {"name": "lux", "version": "1"}})] = "initialize"
    for line in p.stdout:
        m = json.loads(line)
        if "method" in m and "id" in m:
            # A server request (an approval): approved, as lux does.
            p.stdin.write(json.dumps({"id": m["id"], "result": {"decision": "approved"}}) + "\n")
            p.stdin.flush()
            records.append({"type": "codex.request." + m["method"], "data": m.get("params")})
            continue
        if "id" in m:
            what = waiting.pop(m["id"], "")
            if "error" in m:
                sys.exit(f"codex {what}: {m['error']}")
            if what == "initialize":
                waiting[call("thread/start", {"cwd": str(ROOT)})] = "thread/start"
            elif what == "thread/start":
                thread = m["result"]["thread"]["id"]
                waiting[call("turn/start", {"threadId": thread, "clientUserMessageId": "prompt",
                                            "input": [{"type": "text", "text": QUESTION}]})] = "turn/start"
            continue
        method, params = m.get("method"), m.get("params")
        if method == "thread/tokenUsage/updated":
            usage = params.get("tokenUsage")
        if method == "turn/completed":
            data = {"status": params["turn"].get("status")}
            if usage is not None:
                data["usage"] = usage
            records.append({"type": "codex.turn_end", "data": data})
        records.append({"type": "codex." + method, "data": params})
        if method == "turn/completed":
            break
    p.stdin.close()
    try:
        p.wait(timeout=30)
    except subprocess.TimeoutExpired:
        p.kill()
    return records


def translate(path: Path) -> list:
    env = {**os.environ, "DUDE_TRANSLATE_FILE": str(path), "DUDE_TEST_PG": os.environ.get("DUDE_TEST_PG", "127.0.0.1:55951")}
    out = subprocess.run(
        ["go", "-C", str(ROOT / "orchestrator"), "test", "-count=1", "-v", "-run", "^TestPrintATranslation$", "./internal/phases"],
        env=env, capture_output=True, text=True).stdout
    events = [json.loads(line[len("EVENT="):]) for line in out.splitlines() if line.startswith("EVENT=")]
    if not events:
        print(out[-3000:])
    return events


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("harnesses", nargs="*", default=list(MODELS))
    args = ap.parse_args()
    key = os.environ.get("DUDE_LLM_KEY") or sys.exit("DUDE_LLM_KEY is not set")
    url = os.environ.get("DUDE_LLM_URL", DEFAULT_URL)
    mcp = serve_mcp()
    ok = True
    for harness in args.harnesses:
        for attempt in range(1, TRIES + 1):
            print(f"== {harness}, try {attempt}", flush=True)
            if run_one(harness, key, url, mcp):
                break
        else:
            ok = False
    return 0 if ok else 1


def run_one(harness: str, key: str, url: str, mcp: str) -> bool:
    """One real run of harness, recorded and translated; True when it has a thought with text, a message and a tool call."""
    Path(SCRATCH).parent.mkdir(parents=True, exist_ok=True)
    Path(SCRATCH).unlink(missing_ok=True)
    spec = harness_spec(harness, url)
    print(f"   {' '.join(spec['command'])}")
    print("   env:", {k: v for k, v in spec["env"].items() if not k.startswith(("GIT_", "TERM", "FORCE", "CLICOLOR", "PY_"))},
          "secrets:", sorted(spec["secrets"]))
    with tempfile.TemporaryDirectory(prefix="hx-home-") as home:
        records = (claude if harness == "claude-code" else codex)(spec, key, home, mcp)
    text = "\n".join(json.dumps(r, sort_keys=True) for r in records).replace(key, "<key>") + "\n"
    path = TESTDATA / f"{harness}-real.jsonl"
    path.write_text(text)
    print(f"   {len(records)} records -> {path.relative_to(ROOT)}; MCP calls: {[c.get('name') for c in MCP.calls]}")
    MCP.calls.clear()
    events = translate(path)
    kinds: dict = {}
    for e in events:
        kinds[e["type"]] = kinds.get(e["type"], 0) + 1
    print("   dude events:", kinds)
    for e in events:
        p = e["payload"]
        if e["type"] == "agent.thought":
            print("   thought:", p["text"][:300].replace("\n", " "))
        elif e["type"] == "agent.message":
            print("   message:", p["text"][:300].replace("\n", " "))
        elif e["type"] == "agent.tool.called":
            print("   tool:", p["tool"], json.dumps(p.get("input"))[:120])
        elif e["type"] == "agent.plan.updated":
            print("   plan:", json.dumps(p["todos"])[:200])
        elif e["type"] == "agent.model.request.completed" and p.get("turn"):
            print("   turn end:", json.dumps(p))
    thought = any(e["type"] == "agent.thought" and e["payload"]["text"].strip() for e in events)
    if not (thought and kinds.get("agent.message") and kinds.get("agent.tool.called")):
        print(f"   not yet: {harness} needs a thought with text, a message and a tool call", flush=True)
        return False
    print(f"   OK: {harness}", flush=True)
    return True


if __name__ == "__main__":
    sys.exit(main())
