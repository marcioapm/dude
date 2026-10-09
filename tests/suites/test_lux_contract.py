"""The contract with lux: dude against a real lux, not the stand-in.

Everything else in the suite runs against `fake-lux`, which is what makes it
fast and deterministic. That is only sound if the stand-in behaves like lux
where dude depends on it: the spec it accepts, the order its stream reports
things in, what a push reports, what stop and resume do. These tests run the
same flows through a real lux — containers on Podman hosts, lux-fake as the
agent — so a drift between the two shows up here first.

Opt-in, and in an environment of its own: `run_tests.py --lux` points the
orchestrator at a real lux (by default the latest `run_tests.py --serve` in
lux's repository; DUDE_TEST_LUX_ENV names another) and runs only these.
"""

from __future__ import annotations

import json
import os
import re

import pytest
import requests

from fake_github import FakeGitHub
from helpers import RESUME_STARTED, RESUME_STOPPED, ApiClient, assert_timed_is_its_row, execute, lux_stamp, query, wait_until

pytestmark = pytest.mark.lux

# lux-fake, preloaded on every lux host. dude writes its scripts
# (fakeScript in orchestrator/internal/phases/spec.go).
FAKE_IMAGE = "localhost/lux-fake:test"


@pytest.fixture
def lux_project(client: ApiClient, env) -> tuple[dict, FakeGitHub]:
    """A project whose repository lux's hosts can reach: they are containers,
    and see this machine at their network's gateway."""
    gh = FakeGitHub(env.git_root, owner=f"o{os.urandom(4).hex()}", listen=env.real_lux["gateway"])
    gh.start()
    resp = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token", "apiBaseUrl": gh.api_url})
    assert resp.status_code == 200, resp.text
    project = client.create_project(
        name="On lux", slug=f"lux-{os.urandom(3).hex()}",
        agentModels=client.on_models({r: "fake/scripted" for r in ("implementer", "reviewer", "simplifier")}),
        repositories=[{"name": "target", "url": gh.clone_url, "defaultBranch": "main"}],
    )
    yield project, gh
    gh.stop()


def test_a_delivery_runs_on_real_lux(client: ApiClient, lux_project):
    """Implement → review → fix → review → simplify → PR, every agent a lux Run.

    lux-fake commits for real inside its container; lux pushes over git; dude
    fast-forwards the task's branch and opens the PR.
    """
    project, gh = lux_project
    task = client.create_task(project["id"], "Deliver on lux")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    def pr_open():
        return client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"]

    try:
        pr = wait_until(pr_open, timeout=300, interval=2, message="no pull request on real lux")[0]
    finally:
        print("phases:", [(r["phase"], r["status"], r.get("error")) for r in client.task_runs(task["id"])])

    phases = [(r["phase"], r["status"]) for r in client.task_runs(task["id"])]
    assert phases == [("implement", "completed"), ("review", "completed"), ("fix", "completed"),
                      ("review", "completed"), ("simplify", "completed")], phases

    # The commits lux-fake made inside the containers are on the branch.
    log = gh.branch_log(pr["headBranch"])
    assert any("Add FACTORY.md" in m for m in log), log
    assert any("Address review findings" in m for m in log), log

    # The review's findings came through lux's stream intact.
    findings = client.get("/v1/findings", params={"taskId": task["id"]}).json()["findings"]
    assert [(f["severity"], f["status"]) for f in findings] == [("blocking", "resolved")], findings

    # What dude recorded from the stream is a conversation, not lux internals.
    implement = client.task_runs(task["id"])[0]
    events = client.events(runId=implement["id"])
    types = {e["eventType"] for e in events}
    assert {"agent.session.started", "agent.message", "run.completed", "git.commit_created"} <= types, types

    # lux acknowledges the task itself, with what the agent received: the
    # scripted agent's prompt is its script.
    prompts = [e["payload"] for e in events if e["eventType"] == "agent.prompt.delivered"]
    assert len(prompts) == 1 and "commit" in prompts[0]["text"], prompts
    # And relays the turn's token usage, which only the prompt's response carries.
    usage = client.get(f"/v1/runs/{implement['id']}").json()["tokens"]
    assert usage["output"] > 0, usage

    # What the agent published with lux-shim publish (lux#77), reported on
    # the Run's stream as artifact.published, recorded by dude with its
    # description and version, and read back through lux.
    def notes():
        found = client.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"]
        return [a for a in found if a["name"] == "NOTES.md"]

    art = wait_until(notes, timeout=60, interval=1, message="lux's artifacts never reached dude")[0]
    assert art["runId"] == implement["id"] and art["sizeBytes"] > 0, art
    assert (art["description"], art["version"]) == ("What NOTES.md is for", 1), art
    content = client.get(f"/v1/artifacts/{art['id']}/content")
    assert content.status_code == 200 and content.text.startswith("# What changed"), (content.status_code, content.text)
    # The beforeStop hook published the final diff with lux-shim too: the
    # Run's diff, never one of its files.
    assert client.get(f"/v1/runs/{implement['id']}/diff").json()["final"]
    listed = client.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"]
    assert not [a for a in listed if a["name"].startswith(".dude")], listed


def test_steering_pause_and_resume_on_real_lux(client: ApiClient, lux_project):
    """A live agent on lux hears a directive, stops, and continues its session."""
    project, _ = lux_project
    # An agent that never finishes its turn, to have something live to control.
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": client.on_models({"implementer": "fake/hang"})})
    task = client.create_task(project["id"], "Hold")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    run = wait_until(
        lambda: next((r for r in client.task_runs(task["id"]) if r["status"] == "running"), None),
        timeout=120, interval=1, message="the agent never started on lux",
    )
    # The agent is mid-turn and speaks ACP, which cannot take a message then:
    # interrupting is how it hears the directive now.
    assert client.post(f"/v1/runs/{run['id']}/steer", {"text": "echo steered", "interrupt": True}).status_code == 201
    wait_until(
        lambda: any(d["deliveredAt"] for d in client.get(f"/v1/runs/{run['id']}/directives").json()["directives"]),
        timeout=60, interval=1, message="lux never acknowledged the directive",
    )

    assert client.post(f"/v1/runs/{run['id']}/pause", {}).status_code == 200
    wait_until(lambda: client.get(f"/v1/runs/{run['id']}").json()["status"] == "paused",
               timeout=90, interval=1, message="the run never paused on lux")

    assert client.post(f"/v1/runs/{run['id']}/resume", {}).status_code == 200
    wait_until(lambda: client.get(f"/v1/runs/{run['id']}").json()["status"] == "running",
               timeout=120, interval=1, message="the run never resumed on lux")

    # One session across both placements: the resume continued it.
    events = client.events(runId=run["id"])
    assert sum(e["eventType"] == "agent.session.started" for e in events) == 1

    assert client.post(f"/v1/runs/{run['id']}/abort", {}).status_code == 200


def _png(width: int, height: int) -> bytes:
    import struct
    import zlib

    raw = b"".join(b"\x00" + bytes((x * 7) % 256 for x in range(width * 3)) for _ in range(height))

    def chunk(kind: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)

    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b""))


def _upload_png(client: ApiClient, task_id: str, name: str, data: bytes) -> dict:
    res = requests.post(f"{client.base_url}/v1/tasks/{task_id}/attachments",
                        files={"original": (name, data, "image/png"), "delivered": (name, data, "image/png")},
                        data={"originalType": "image/png", "deliveredType": "image/png", "name": name},
                        headers={"authorization": f"Bearer {client.api_key}"}, timeout=30)
    assert res.status_code == 201, res.text
    return res.json()


def test_an_image_steer_and_an_image_prompt_on_real_lux(client: ApiClient, lux_project):
    """lux feat/input-attachments: a steer's image goes on POST /input as
    `attachments`, and the task's on `workload.attachments`. lux takes both
    (the spec is validated at submit, so a Run that starts took the prompt's).

    lux-fake speaks ACP: if it does not advertise `promptCapabilities.image`
    lux accepts the input and then fails it with "the agent does not take
    images"; that, or delivery, is what the contract allows. Anything else —
    a 400 invalid_attachment, which dude shows as "lux refused its images",
    or a steer that never settles — is a contract break.
    """
    project, _ = lux_project
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": client.on_models({"implementer": "fake/hang"})})
    task = client.create_task(project["id"], "See this on lux")
    prompt_image = _upload_png(client, task["id"], "design.png", _png(64, 48))
    # A task's images are the ones its text shows: referenced, it is attached for every agent's prompt.
    res = client.patch(f"/v1/tasks/{task['id']}",
                       {"goal": f"{client.DEFAULT_GOAL}\n\n![design.png](attachment:{prompt_image['id']})"})
    assert res.status_code == 200, res.text
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    run = wait_until(
        lambda: next((r for r in client.task_runs(task["id"]) if r["status"] == "running"), None),
        timeout=120, interval=1, message="the agent given a prompt image never started on lux",
    )
    prompt = wait_until(lambda: [e for e in client.events(runId=run["id"]) if e["eventType"] == "agent.prompt.delivered"],
                        timeout=60, interval=1, message="lux never acknowledged the prompt")
    assert [a["id"] for a in prompt[0]["payload"].get("attachments", [])] == [prompt_image["id"]]

    steer_image = _upload_png(client, task["id"], "checkout.png", _png(120, 80))
    res = client.post(f"/v1/runs/{run['id']}/steer", {"text": "see this", "interrupt": True, "attachmentIds": [steer_image["id"]]})
    assert res.status_code == 201, res.text
    directive = res.json()["id"]

    def settled():
        d = next(d for d in client.get(f"/v1/runs/{run['id']}/directives").json()["directives"] if d["id"] == directive)
        if d["deliveredAt"]:
            return ("delivered", None)
        failed = [e for e in client.events(runId=run["id"]) if e["eventType"] == "run.directive.failed"
                  and e["payload"].get("directiveId") == directive]
        return ("failed", failed[0]["payload"]["error"]) if failed else None

    outcome = wait_until(settled, timeout=90, interval=1, message="lux never settled the image steer")
    assert outcome in (("delivered", None), ("failed", "the agent does not take images")), outcome
    assert client.post(f"/v1/runs/{run['id']}/abort", {}).status_code == 200


# ---------------------------------------------------------------------------
# A real model on real lux: does the agent see a task's inline images?
# Opt-in twice over: --lux, and DUDE_LLM_KEY (with DUDE_LLM_URL) for the
# orchestrator. DUDE_TEST_AGENT_IMAGE is dude's runtime image as lux's hosts
# hold it (scripts/runtime-image.sh, preloaded with lux's --serve --image);
# DUDE_TEST_VISION_MODEL the model, as the proxy names it.
# ---------------------------------------------------------------------------

AGENT_IMAGE = os.environ.get("DUDE_TEST_AGENT_IMAGE", "localhost/dude-runtime:dev")
VISION_MODEL = os.environ.get("DUDE_TEST_VISION_MODEL", "claude-sonnet-5")
IMAGES = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "fixtures", "images")
needs_model = pytest.mark.skipif(not os.environ.get("DUDE_LLM_KEY"), reason="needs DUDE_LLM_KEY and DUDE_LLM_URL: a real model")

# What the agent is asked; the images' content is in no text it is given.
SEEN_GOAL = (
    "Create a file SEEN.md at the root of the repository that describes exactly what each image given "
    "with this task shows: every shape, its colour, the background colour, and any text, word for word. "
    "Put each image under its own heading, `## Image N`, with the number the task gives it. If you were "
    "given no images, say so in SEEN.md instead. Change nothing else, and commit SEEN.md."
)


def _vision_project(client: ApiClient, env, lux_project) -> tuple[dict, FakeGitHub]:
    """lux_project with every role on the real model, in dude's runtime image."""
    project, gh = lux_project
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": client.on_models(
        {r: VISION_MODEL for r in ("implementer", "reviewer", "simplifier")})})
    # The API takes a library image only; a typed ref is the operator's.
    query(env.owner_dsn, "UPDATE projects SET runtime_image = %s WHERE id = %s RETURNING id", (AGENT_IMAGE, project["id"]))
    return project, gh


def _fixture_png(name: str) -> bytes:
    with open(os.path.join(IMAGES, name), "rb") as f:
        return f.read()


def _lux_inputs(env, lux_run_id: str) -> list[dict]:
    """The lux.input records of a lux Run's output, as its shim wrote them."""
    res = _lux(env, "GET", f"/v1/runs/{lux_run_id}/output")
    assert res.status_code == 200, res.text
    out, event = [], ""
    # text/event-stream names no charset; requests would read it as Latin-1.
    for line in res.content.decode("utf-8").splitlines():
        if line.startswith("event:"):
            event = line[6:].strip()
        elif line.startswith("data:") and event == "record":
            rec = json.loads(line[5:])
            if (rec.get("event") or {}).get("type") == "lux.input":
                out.append(rec["event"]["data"])
    return out


def _messages(client: ApiClient, run_id: str) -> str:
    return "\n".join(e["payload"].get("text", "") for e in client.events(runId=run_id, limit=1000)
                     if e["eventType"] == "agent.message")


def _plumbing(client: ApiClient, env, run: dict, ids: list[str]) -> dict:
    """What dude sent lux for a Run and what lux and dude recorded of it: the
    prompt, agent.prompt.delivered's attachments, lux's prompt input record."""
    lux_run_id = query(env.owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (run["id"],))[0]["lux_run_id"]
    prompt = _lux(env, "GET", f"/v1/runs/{lux_run_id}").json()["spec"]["workload"]["prompt"]
    delivered = [e["payload"] for e in client.events(runId=run["id"], limit=1000) if e["eventType"] == "agent.prompt.delivered"]
    inputs = [r for r in _lux_inputs(env, lux_run_id) if r.get("requestId") == "prompt"]
    print(f"--- {run['phase']} {run['id']} (lux {lux_run_id})")
    print("prompt:", prompt)
    print("agent.prompt.delivered attachments:", json.dumps([d.get("attachments") for d in delivered]))
    print("lux.input prompt:", json.dumps(inputs))
    assert len(delivered) == 1, delivered
    assert [a["id"] for a in delivered[0].get("attachments", [])] == ids, delivered
    assert len(inputs) == 1 and inputs[0].get("phase") == "accepted", inputs
    assert [a["name"] for a in inputs[0].get("attachments", [])] == ["a.png", "b.png"][:len(ids)], inputs
    return {"prompt": prompt, "delivered": delivered[0], "input": inputs[0]}


def _section(text: str, n: int) -> str:
    """What SEEN.md says under its Image n heading, up to the next one."""
    m = re.search(rf"^#+[^\n]*Image\s*{n}\b[^\n]*\n(.*?)(?=^#+[^\n]*Image\s*\d|\Z)", text, re.M | re.S)
    assert m, f"no Image {n} heading in SEEN.md:\n{text}"
    return m.group(1).lower()


@needs_model
@pytest.mark.timeout(1500)
def test_a_real_model_describes_a_tasks_inline_images(client: ApiClient, env, lux_project):
    """The agent is told about two images only by their place in the task's
    text; what they show is in their pixels alone. It writes down what it
    sees, and that is right: so the images reached the model, numbered as the
    prompt numbers them. The reviewer is given them too."""
    project, gh = _vision_project(client, env, lux_project)
    task = client.create_task(project["id"], "Describe the images")
    a = _upload_png(client, task["id"], "a.png", _fixture_png("inline-a.png"))
    b = _upload_png(client, task["id"], "b.png", _fixture_png("inline-b.png"))
    res = client.patch(f"/v1/tasks/{task['id']}", {
        "goal": f'{SEEN_GOAL}\n\nThe first image ![a.png](attachment:{a["id"]} "small right") is the one to start with.',
        "acceptanceCriteria": ["SEEN.md has a section for each image",
                               f"The second image's words are in SEEN.md\n![b.png](attachment:{b['id']})"],
    })
    assert res.status_code == 200, res.text
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    def pr_open():
        return client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"]

    try:
        pr = wait_until(pr_open, timeout=1200, interval=5, message="no pull request from the real model")[0]
    finally:
        print("phases:", [(r["phase"], r["status"], r.get("error")) for r in client.task_runs(task["id"])])
    seen = gh._git_out("show", f"refs/heads/{pr['headBranch']}:SEEN.md")
    print("SEEN.md:\n" + seen)

    runs = client.task_runs(task["id"])
    implement = next(r for r in runs if r["phase"] == "implement")
    plumbing = _plumbing(client, env, implement, [a["id"], b["id"]])
    # Each reference is its number and name, in place; the layout words and the URL are not the agent's.
    assert "The first image [Image 1: a.png] is the one to start with." in plumbing["prompt"], plumbing["prompt"]
    assert "[Image 2: b.png]" in plumbing["prompt"], plumbing["prompt"]
    assert "small" not in plumbing["prompt"] and "attachment:" not in plumbing["prompt"], plumbing["prompt"]
    # Nothing it was told names what the images show.
    for word in ("pelican", "7342", "orbit", "5150", "triangle", "circle"):
        assert word not in plumbing["prompt"].lower(), word

    first, second = _section(seen, 1), _section(seen, 2)
    assert "7342" in first and "triangle" in first, first
    assert any(c in first for c in ("red", "green")), first
    assert "5150" in second and "circle" in second, second
    assert any(c in second for c in ("blue", "white")), second
    assert "7342" not in second and "5150" not in first, seen

    # The reviewer checks the work against the same task, images and all.
    review = next(r for r in runs if r["phase"] == "review")
    _plumbing(client, env, review, [a["id"], b["id"]])
    print("reviewer said:\n" + _messages(client, review["id"]))


@needs_model
@pytest.mark.timeout(900)
def test_without_references_a_real_model_is_given_no_images(client: ApiClient, env, lux_project):
    """The negative control: the same images uploaded to the task, but no
    text shows them. The agent is given none, and cannot know their words."""
    project, _ = _vision_project(client, env, lux_project)
    task = client.create_task(project["id"], "Describe no images")
    _upload_png(client, task["id"], "a.png", _fixture_png("inline-a.png"))
    _upload_png(client, task["id"], "b.png", _fixture_png("inline-b.png"))
    res = client.patch(f"/v1/tasks/{task['id']}", {"goal": SEEN_GOAL, "acceptanceCriteria": ["SEEN.md has a section for each image"]})
    assert res.status_code == 200, res.text
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    def implemented():
        run = next((r for r in client.task_runs(task["id"]) if r["phase"] == "implement"), None)
        return run if run and run["status"] in ("completed", "failed", "aborted") else None

    try:
        run = wait_until(implemented, timeout=600, interval=5, message="the implementer never finished")
    finally:
        print("phases:", [(r["phase"], r["status"], r.get("error")) for r in client.task_runs(task["id"])])
    said = _messages(client, run["id"])
    print("implementer said:\n" + said)
    lux_run_id = query(env.owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (run["id"],))[0]["lux_run_id"]
    inputs = [r for r in _lux_inputs(env, lux_run_id) if r.get("requestId") == "prompt"]
    print("lux.input prompt:", json.dumps(inputs))
    assert inputs and not inputs[0].get("attachments"), inputs
    assert not any(w in said.lower() for w in ("7342", "5150", "pelican", "orbit")), said
    for r in client.task_runs(task["id"]):
        if r["status"] not in ("completed", "failed", "aborted"):
            client.post(f"/v1/runs/{r['id']}/abort", {})


@pytest.mark.skipif(not os.environ.get("DUDE_TEST_TOOLS_HOST"), reason="needs DUDE_TEST_TOOLS_HOST: an address of this machine lux's hosts can reach")
def test_an_agent_on_real_lux_calls_dudes_tools(client: ApiClient, lux_project):
    """lux hands the agent dude's MCP server, authenticated as its Run; the
    agent (lux-fake's MCP client) calls list_tasks, and dude records the
    call on that Run and answers with the project's work."""
    project, _ = lux_project
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": client.on_models({
        "implementer": "fake/tools", "reviewer": "fake/scripted", "simplifier": "fake/scripted"})})
    task = client.create_task(project["id"], "Use the tools")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    def called():
        runs = client.task_runs(task["id"])
        if not runs:
            return None
        return [e for e in client.events(runId=runs[0]["id"]) if e["eventType"] == "agent.tool.dude"] or None

    try:
        calls = wait_until(called, timeout=120, interval=1, message="the agent never called dude's tools on lux")
    finally:
        print("phases:", [(r["phase"], r["status"], r.get("error")) for r in client.task_runs(task["id"])])
    assert calls[0]["payload"]["tool"] == "list_tasks"
    assert "Use the tools" in json.dumps(calls[0]["payload"]["result"])
    # And through lux's service socket, as the dude CLI calls: lux added the
    # Run's token; the container never held it.
    progress = wait_until(lambda: [e for e in client.events(runId=calls[0]["runId"])
                                   if e["eventType"] == "agent.custom.progress"] or None,
                          timeout=60, interval=1, message="nothing came through lux's service socket")
    assert progress[0]["payload"]["data"]["step"] == "through the socket"
    # And the agent heard the answer: its reply, which comes after the
    # socket call, carries the task's key.
    implement = client.task_runs(task["id"])[0]

    def replied():
        text = " ".join(e["payload"].get("text", "") for e in client.events(runId=implement["id"])
                        if e["eventType"] == "agent.message")
        return text if task["key"] in text else None

    wait_until(replied, timeout=60, interval=1, message="the agent's reply never named the task")


@pytest.mark.skipif(not os.environ.get("DUDE_TEST_TOOLS_HOST"), reason="needs DUDE_TEST_TOOLS_HOST: an address of this machine lux's hosts can reach")
def test_an_agent_waiting_on_a_person_is_parked_on_real_lux_and_resumed_by_the_answer(client: ApiClient, env, lux_project):
    """The agent asks with ask_person through lux's service socket and ends
    its turn; past the grace period dude stops the lux Run (nothing held);
    the answer resumes the same lux Run, in the same agent session, and the
    resume is timed with every timestamp lux reports, in order."""
    project, _ = lux_project
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": client.on_models({
        "implementer": "fake/ask", "reviewer": "fake/scripted", "simplifier": "fake/scripted"})})
    task = client.create_task(project["id"], "Ask on lux")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    def implementer():
        return next((r for r in client.task_runs(task["id"]) if r["phase"] == "implement"), None)

    try:
        wait_until(lambda: (implementer() or {}).get("status") == "paused", timeout=180, interval=1,
                   message="the waiting agent was never parked on lux")
        stopped_epoch = _stopped_epoch(client, env, implementer()["id"])
        question = client.get("/v1/questions").json()["questions"]
        question = next(q for q in question if q["taskId"] == task["id"])
        assert client.post(f"/v1/questions/{question['id']}/answer", {"text": "yes"}).status_code == 200
        wait_until(lambda: implementer()["status"] == "completed", timeout=180, interval=1,
                   message="the answer did not resume the parked agent on lux")
    finally:
        print("phases:", [(r["phase"], r["status"], r.get("error")) for r in client.task_runs(task["id"])])
    types = [e["eventType"] for e in client.events(runId=implementer()["id"])]
    assert "run.parked" in types and "run.unparked" in types, types
    # One agent session, continued: resumed, not started over.
    assert types.count("agent.session.started") == 1, types
    messages = " ".join(e["payload"].get("text", "") for e in client.events(runId=implementer()["id"])
                        if e["eventType"] == "agent.message")
    assert "Answer to your question" in messages, messages
    _resume_timed_on_lux(client, env, implementer()["id"], "answer", stopped_epoch)


def _stopped_epoch(client: ApiClient, env, run_id: str) -> int:
    """The epoch lux stopped the parked or paused Run in, read from lux once
    it reports the Run stopped, before anything resumes it."""
    lux_run_id = query(env.owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (run_id,))[0]["lux_run_id"]
    run = wait_until(lambda: (r := _lux(env, "GET", f"/v1/runs/{lux_run_id}").json())["state"] == "stopped" and r,
                     timeout=120, interval=1, message="lux never reported the Run stopped")
    return run["epoch"]


def _resume_timed_on_lux(client: ApiClient, env, run_id: str, cause: str, stopped_epoch: int) -> dict:
    """A resume on real lux is recorded under the epoch of lux's placement
    that started after the resume — the first above stopped_epoch, the one
    it was stopped in, which is also the epoch lux's answer to the resume
    carries — with every field lux reports of both placements, as lux
    reports them, and timed once with what the row says."""
    timed = wait_until(lambda: [e for e in client.events(runId=run_id) if e["eventType"] == "run.resume.timed"],
                       timeout=120, interval=1, message="the resume on lux was never timed")
    rows = query(env.owner_dsn, "SELECT r.*, runs.lux_run_id FROM run_resumes r JOIN runs ON runs.id = r.run_id "
                                "WHERE r.run_id = %s", (run_id,))
    print("resume:", rows, "timed:", [e["payload"] for e in timed])
    assert len(rows) == 1 and len(timed) == 1, (rows, timed)
    row = rows[0]
    placements = _lux(env, "GET", f"/v1/runs/{row['lux_run_id']}").json()["placements"]
    after = min(p["epoch"] for p in placements if p["epoch"] > stopped_epoch)
    assert row["cause"] == cause and row["epoch"] == after and row["epoch"] > stopped_epoch, (row, stopped_epoch, placements)
    unparked = [e["payload"].get("epoch") for e in client.events(runId=run_id) if e["eventType"] == "run.unparked"]
    assert all(epoch == row["epoch"] for epoch in unparked), (unparked, row["epoch"])
    missing = [c for c in ("woken_at", "requested_at", "running_at", "busy_at", "first_output_at", "moved")
               if row[c] is None]
    assert missing == [], f"dude left {missing} unrecorded: {row}"
    assert row["frames_missed"] is False, row
    # The new placement is the resume's epoch, the stopped one the latest
    # before it, as lux reports them now.
    resumed = next(p for p in placements if p["epoch"] == row["epoch"])
    stopped = next(p for p in placements if p["epoch"] == stopped_epoch)
    for placement, fields in ((resumed, RESUME_STARTED), (stopped, RESUME_STOPPED)):
        for column, field in fields:
            want = placement.get(field)
            assert want is not None, f"lux left {field} of epoch {placement['epoch']} unreported: {placement}"
            if column.endswith("_at"):
                want = lux_stamp(want)
            assert row[column] == want, (column, row[column], field, want)
    # Within one placement's own clock (its host's), its stamps are in order;
    # different hosts' clocks, and luxd's, are not compared.
    for placement, fields in ((resumed, RESUME_STARTED), (stopped, RESUME_STOPPED)):
        stamps = [row[c] for c, _ in fields if c.endswith("_at")]
        assert stamps == sorted(stamps), (placement["epoch"], stamps)
    # dude's own, one clock: Postgres's.
    dude = [row[c] for c in ("woken_at", "requested_at", "running_at", "busy_at", "first_output_at")]
    assert dude == sorted(dude), dude
    assert row["snapshot_bytes"] > 0, row
    assert row["moved"] == (row["host_name"] != row["stopped_host_name"]), row
    assert_timed_is_its_row(timed[0]["payload"], row)
    return row


def test_a_person_pause_and_resume_on_real_lux_is_timed(client: ApiClient, env, lux_project):
    """A person pauses a working agent on lux and resumes it: lux reports
    every placement time of both placements, and dude records them in order
    and times the resume once the agent speaks."""
    project, _ = lux_project
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": {
        "implementer": {"model": "fake/live"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    task = client.create_task(project["id"], "Pause on lux, then carry on")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    run = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["status"] == "running"), None),
                     timeout=120, interval=1, message="the agent never started on lux")
    wait_until(lambda: query(env.owner_dsn, "SELECT 1 FROM runs WHERE id = %s AND lux_state = 'running'", (run["id"],)),
               timeout=120, interval=1, message="lux never ran the agent")
    try:
        assert client.post(f"/v1/runs/{run['id']}/pause", {}).status_code == 200
        wait_until(lambda: query(env.owner_dsn, "SELECT 1 FROM runs WHERE id = %s AND status = 'paused' AND lux_state = 'stopped'",
                                 (run["id"],)), timeout=120, interval=1, message="lux never stopped the paused run")
        stopped_epoch = _stopped_epoch(client, env, run["id"])
        assert client.post(f"/v1/runs/{run['id']}/resume", {}).status_code == 200
        _resume_timed_on_lux(client, env, run["id"], "person", stopped_epoch)
    finally:
        print("phases:", [(r["phase"], r["status"], r.get("error")) for r in client.task_runs(task["id"])])
        client.post(f"/v1/runs/{run['id']}/abort", {})


def test_a_file_published_on_real_lux_is_listed_while_the_run_keeps_running(client: ApiClient, env, lux_project):
    """The fake/live implementer publishes its notes with lux-shim publish
    and keeps working: lux reports artifact.published once the file can be
    downloaded, and dude lists it, with its description, while the Run is
    still running (lux#77)."""
    project, _ = lux_project
    resp = client.patch(f"/v1/projects/{project['id']}", {"agentModels": client.on_models({
        "implementer": "fake/live", "reviewer": "fake/scripted", "simplifier": "fake/scripted"})})
    assert resp.status_code == 200, resp.text
    task = client.create_task(project["id"], "Publish on lux as you go")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    run = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["status"] == "running"), None),
                     timeout=120, interval=1, message="the agent never started on lux")
    try:
        def notes_while_running():
            found = [a for a in client.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"]
                     if a["name"] == "NOTES.md"]
            return found if found and client.get_run(run["id"])["status"] == "running" else None

        notes = wait_until(notes_while_running, timeout=120, interval=1, message="the notes were not listed while it ran")
        assert [(a["description"], a["version"]) for a in notes] == [("What NOTES.md is for", 1)], notes
        content = client.get(f"/v1/artifacts/{notes[0]['id']}/content")
        assert content.status_code == 200 and "Still working." in content.text, (content.status_code, content.text)
    finally:
        client.post(f"/v1/runs/{run['id']}/abort", {})


# ---------------------------------------------------------------------------
# Machine sizes: what dude sends lux as a Run's size and pool, and how it
# reads lux's pools. Straight to lux's API with dude's key, encoded as the
# orchestrator encodes them (lux.Resources: memory and disk as JSON numbers
# of bytes; lux.PlacementSpec: placement.poolId).
# ---------------------------------------------------------------------------

GIB = 1 << 30


def _lux(env, method: str, path: str, body: dict | None = None, key: str | None = None) -> requests.Response:
    headers = {"authorization": f"Bearer {env.real_lux['api_key']}"}
    if key:
        headers["idempotency-key"] = key
    return requests.request(method, env.real_lux["luxd_url"] + path, json=body, headers=headers, timeout=30)


def _sleeper(name: str, **extra) -> dict:
    """A Run that only waits, as a branch preview's workload does."""
    return {"name": name, "labels": {"dude.kind": "contract"}, "image": {"ref": FAKE_IMAGE},
            "workload": {"adapter": "generic", "command": ["sleep", "infinity"], "workdir": "/tmp"}, **extra}


def test_a_run_sized_as_dude_sizes_it_reaches_running_on_real_lux(env):
    """lux's spec.Bytes takes a JSON number of bytes as well as "8Gi"
    (docs/runspec.md: "Sizes accept 512Mi, 8Gi, 1G, or bytes"); this is
    the number form dude sends. Its hosts must have 2 CPUs and 8 GiB free."""
    resources = {"cpus": 2, "memory": 8 * GIB, "disk": 20 * GIB}
    res = _lux(env, "POST", "/v1/runs", _sleeper("contract sized", resources=resources), key=f"contract-{os.urandom(6).hex()}")
    assert res.status_code in (200, 201, 202), res.text
    run_id = res.json()["id"]
    try:
        run = wait_until(lambda: (r := _lux(env, "GET", f"/v1/runs/{run_id}").json())["state"] == "running" and r,
                         timeout=180, interval=1, message="a Run with dude's resources never ran on lux")
        # lux kept the size it was given, in bytes.
        assert run["spec"]["resources"]["memory"] == 8 * GIB and run["spec"]["resources"]["disk"] == 20 * GIB, run["spec"]
    finally:
        _lux(env, "POST", f"/v1/runs/{run_id}/cancel")


def test_a_pool_id_lux_does_not_have_is_refused(env):
    """What lux does with a size whose pool is gone (deleted in lux after an
    admin chose it). dude sends the pool as placement.poolId; lux resolves
    it at submit and refuses an id it has no pool for with 422
    unknown_pool, which dude turns into a failed Run with a reason in words
    (phases.PoolGone). Needs a lux with placement.poolId (feat/memory-factor)."""
    pool_id = f"pool_nosuch{os.urandom(4).hex()}"
    res = _lux(env, "POST", "/v1/runs", _sleeper("contract no pool", placement={"poolId": pool_id}), key=f"contract-{os.urandom(6).hex()}")
    if res.status_code in (200, 201, 202):
        _lux(env, "POST", f"/v1/runs/{res.json()['id']}/cancel")
    assert res.status_code == 422, (res.status_code, res.text)
    assert res.json()["error"]["code"] == "unknown_pool", res.text


def test_luxs_pools_decode_as_dude_reads_them(env, client: ApiClient):
    res = _lux(env, "GET", "/v1/pools")
    assert res.status_code == 200, res.text
    pools = res.json()["pools"]
    assert all(isinstance(p["name"], str) and p["name"] for p in pools), pools
    # Each pool's id is what a size stores; the pattern is migration 064's.
    assert all(re.fullmatch(r"pool_[A-Za-z0-9_-]+", p.get("id", "")) for p in pools), pools
    for p in pools:
        # Absent from an older lux; when there, one host's size: CPUs, and
        # memory and disk in bytes (0: disk not reserved).
        host = p.get("hostSize")
        if host is not None:
            assert isinstance(host["cpus"], (int, float)) and host["cpus"] > 0, p
            assert isinstance(host["memory"], int) and host["memory"] > 0, p
            assert isinstance(host["disk"], int) and host["disk"] >= 0, p
        assert p.get("hostSizeFrom") in (None, "", "running", "history"), p
        assert isinstance(p.get("isDefault", False), bool) and isinstance(p.get("hostsRunning", 0), (int, type(None))), p
    # And through the orchestrator's lux.Client and the backend, as the
    # Machines page reads them: a decoding failure would be a `problem`.
    served = client.get("/v1/machines/pools")
    assert served.status_code == 200 and served.json()["problem"] is None, served.text
    assert sorted(p["name"] for p in served.json()["pools"]) == sorted(p["name"] for p in pools)


def test_a_role_on_a_size_reaches_real_lux_with_its_resources_and_pool(client: ApiClient, env, owner_dsn: str, lux_project):
    """End to end: an admin's machine size, named by a role, is what lux runs.

    The size names one of lux's pools by id; the implementer's Run goes to
    lux with the size as resources and that pool as placement.poolId. lux
    resolves the pool, places the Run there, starts its container with a
    memory limit scaled from the size, and dude records the size and the
    limit on its Run. Needs a lux with placement.poolId (feat/memory-factor)."""
    project, _gh = lux_project
    pools = client.get("/v1/machines/pools").json()
    assert pools["problem"] is None, pools
    pool = next(p for p in pools["pools"] if p.get("isDefault")) if any(p.get("isDefault") for p in pools["pools"]) else pools["pools"][0]
    made = client.post("/v1/machines/sizes", {"name": f"Contract {os.urandom(2).hex()}", "cpus": 1.5, "memoryMiB": 1536, "diskGiB": 5, "poolId": pool["id"]})
    assert made.status_code == 201, made.text
    size = next(s for s in made.json()["sizes"] if s["name"].startswith("Contract"))
    client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"implementer": {"machineSize": size["id"]}}})

    task = client.create_task(project["id"], "Sized on lux")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    def submitted():
        rows = query(owner_dsn, "SELECT id, lux_run_id, machine FROM runs WHERE task_id = %s AND phase = 'implement' AND lux_run_id IS NOT NULL", (task["id"],))
        return rows[0] if rows else None

    run = wait_until(submitted, timeout=120, interval=1, message="the implementer never reached lux")

    # What lux stored: the size as resources, the pool by id, resolved to its name.
    lux_run = _lux(env, "GET", f"/v1/runs/{run['lux_run_id']}").json()
    assert lux_run["spec"]["resources"]["cpus"] == 1.5, lux_run["spec"]
    assert lux_run["spec"]["resources"]["memory"] == 1536 * 1024 * 1024, lux_run["spec"]
    assert lux_run["spec"]["resources"]["disk"] == 5 * GIB, lux_run["spec"]
    assert lux_run["spec"]["placement"]["poolId"] == pool["id"], lux_run["spec"]
    assert lux_run["spec"]["placement"]["pool"] == pool["name"], lux_run["spec"]

    # lux placed it and started its container with a memory limit from the size.
    def placed():
        r = _lux(env, "GET", f"/v1/runs/{run['lux_run_id']}").json()
        ps = r.get("placements") or []
        return ps[-1] if ps and ps[-1].get("memoryLimit") else None

    placement = wait_until(placed, timeout=180, interval=1, message="lux never started the sized Run's container")
    limit = placement["memoryLimit"]
    assert 0 < limit <= 1536 * 1024 * 1024, placement
    print("placement:", {k: placement.get(k) for k in ("hostId", "memoryLimit")})

    # dude recorded the size it sent, the pool's name then, and lux's limit.
    def recorded():
        m = query(owner_dsn, "SELECT machine FROM runs WHERE id = %s", (run["id"],))[0]["machine"]
        return m if m and m.get("memoryLimit") else None

    machine = wait_until(recorded, timeout=120, interval=1, message="dude never recorded lux's memory limit")
    assert (machine["name"], machine["from"], machine["poolId"], machine["pool"]) == (size["name"], "project", pool["id"], pool["name"]), machine
    assert (machine["cpus"], machine["memoryMiB"], machine["diskGiB"]) == (1.5, 1536, 5), machine
    assert machine["memoryLimit"] == limit, (machine, placement)


def test_a_paused_agent_resumes_on_the_size_its_role_names_now(client: ApiClient, env, owner_dsn: str, lux_project):
    """lux#51 end to end: an agent paused on one size, its role moved to a
    smaller one in the same pool, resumes on it without a restart. dude
    sends resources on the resume; lux writes what it applied into the
    Run's spec (cpus and memory as asked; the disk only if the saved state
    fits, else it keeps it and says why); dude records lux's spec, not its
    request, and the resumed container gets the new memory."""
    project, _gh = lux_project
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": client.on_models(
        {"implementer": "fake/hang", "reviewer": "fake/scripted", "simplifier": "fake/scripted"})})
    sizes = {}
    for name, cpus, mem, disk in (("Medium", 2, 2048, 10), ("Small", 1, 1024, 5)):
        made = client.post("/v1/machines/sizes", {"name": f"{name} {os.urandom(2).hex()}", "cpus": cpus, "memoryMiB": mem, "diskGiB": disk})
        assert made.status_code == 201, made.text
        sizes[name] = next(s for s in made.json()["sizes"] if s["name"].startswith(name + " "))
    client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"implementer": {"machineSize": sizes["Medium"]["id"]}}})

    task = client.create_task(project["id"], "Resized on lux")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    run = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["status"] == "running"), None),
                     timeout=120, interval=1, message="the agent never started on lux")
    try:
        wait_until(lambda: query(owner_dsn, "SELECT 1 FROM runs WHERE id = %s AND lux_state = 'running'", (run["id"],)),
                   timeout=120, interval=1, message="lux never ran the agent")
        lux_id = query(owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (run["id"],))[0]["lux_run_id"]
        assert client.post(f"/v1/runs/{run['id']}/pause", {}).status_code == 200
        wait_until(lambda: query(owner_dsn, "SELECT 1 FROM runs WHERE id = %s AND status = 'paused' AND lux_state = 'stopped'",
                                 (run["id"],)), timeout=120, interval=1, message="lux never stopped the paused run")

        client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"implementer": {"machineSize": sizes["Small"]["id"]}}})
        assert client.post(f"/v1/runs/{run['id']}/resume", {}).status_code == 200

        def resized():
            m = query(owner_dsn, "SELECT machine FROM runs WHERE id = %s", (run["id"],))[0]["machine"]
            return m if m["name"] == sizes["Small"]["name"] else None

        machine = wait_until(resized, timeout=120, interval=1, message="dude never recorded the new size")
        requested = [e for e in _lux(env, "GET", f"/v1/runs/{lux_id}/events").json()["events"] if e["type"] == "resume.requested"]
        assert len(requested) == 1, requested
        resize = requested[0]["data"]["resources"]
        assert resize["requested"] == {"cpus": 1, "memory": 1024 << 20, "disk": 5 * GIB}, resize
        spec = _lux(env, "GET", f"/v1/runs/{lux_id}").json()["spec"]["resources"]
        applied = resize["applied"]
        assert (spec["cpus"], spec["memory"], spec["disk"]) == (1, 1024 << 20, applied["disk"]), (spec, resize)
        assert (applied["cpus"], applied["memory"]) == (1, 1024 << 20), resize
        # What dude recorded is lux's spec: the disk lux kept, when it kept one.
        assert (machine["cpus"], machine["memoryMiB"], machine["diskGiB"]) == (spec["cpus"], spec["memory"] >> 20, spec["disk"] >> 30), (machine, spec)
        assert machine["from"] == "project" and machine.get("note") is None, machine
        if "disk" in resize:
            assert spec["disk"] == 10 * GIB and machine["diskKept"] == {"requestedGiB": 5, "reason": resize["disk"]["reason"]}, (machine, resize)
        else:
            assert spec["disk"] == 5 * GIB and "diskKept" not in machine, (machine, resize)
        print("resize:", resize)

        # The resumed container has the new memory, and dude says so.
        def placed():
            ps = _lux(env, "GET", f"/v1/runs/{lux_id}").json().get("placements") or []
            return ps[-1] if len(ps) >= 2 and ps[-1].get("memoryLimit") else None

        placement = wait_until(placed, timeout=180, interval=1, message="lux never started the resized container")
        assert 0 < placement["memoryLimit"] <= 1024 << 20, placement
    finally:
        print("phases:", [(r["phase"], r["status"], r.get("error")) for r in client.task_runs(task["id"])])
        client.post(f"/v1/runs/{run['id']}/abort", {})


# ---------------------------------------------------------------------------
# A Run's stage (lux#81): dude reads stage and stageSince from lux's Run and
# hears each stage event as servers.changed.
# ---------------------------------------------------------------------------


def test_a_previews_stage_and_its_timer_are_luxs(client: ApiClient, env, org: dict, owner_dsn: str, lux_project):
    """A branch preview no host can take (a size of 128 CPUs, past what the
    test runners offer) stays at lux's waiting stage. dude's view shows it
    as scheduling, timed from lux's own stageSince, and lux's stage event
    reached dude's stream as a servers.changed of change "stage"."""
    project, _gh = lux_project
    size = f"msz_{os.urandom(6).hex()}"
    execute(owner_dsn, "INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES (%s, %s, %s, 128, 1024, 5)",
            (size, org["id"], f"Huge {size[-4:]}"))
    execute(owner_dsn, "UPDATE projects SET preview_settings = jsonb_build_object('machineSize', %s::text) WHERE id = %s", (size, project["id"]))
    task = client.create_task(project["id"], "Preview stages on lux")
    started = client.post(f"/v1/tasks/{task['id']}/preview")
    assert started.status_code == 201, started.text
    run_id = started.json()["run"]["id"]
    try:
        def waiting():
            run = client.get(f"/v1/tasks/{task['id']}/servers").json()["run"]
            if not run.get("luxRunId"):
                return None
            lr = _lux(env, "GET", f"/v1/runs/{run['luxRunId']}").json()
            return lr["stage"] == "waiting" and (run, lr)

        run, lr = wait_until(waiting, timeout=60, interval=1, message="the preview never reached lux's waiting stage")
        assert run["wakeable"] is False and lr["state"] in ("submitted", "scheduled"), (run, lr)
        assert run["previewStage"] == "scheduling", run
        assert run["previewStageSince"] and lux_stamp(run["previewStageSince"]) == lux_stamp(lr["stageSince"]), (run, lr)

        stages = [e for e in _lux(env, "GET", f"/v1/runs/{lr['id']}/events").json()["events"] if e["type"] == "stage"]
        assert stages and stages[0]["data"]["stage"] == "waiting", stages
        changed = wait_until(lambda: query(owner_dsn, """SELECT count(*) AS n FROM events WHERE run_id = %s AND event_type = 'servers.changed'
                                                          AND payload->>'change' = 'stage'""", (run_id,))[0]["n"] or None,
                             timeout=30, interval=0.5, message="lux's stage event never became servers.changed")
        assert changed == len(stages), (changed, stages)
    finally:
        client.delete(f"/v1/tasks/{task['id']}/preview")
