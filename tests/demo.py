"""A seeded dude to look at and click through: `uv run python demo.py`.

Everything is local and nothing costs money: its own database on the dev
Postgres, its own bucket on a local S3 (for photos), the fake lux running the scripted agents, a fake GitHub that opens
real pull requests on local repositories and sends signed webhooks, and the
built web app. It seeds one organisation with four people and two projects
with epics and tasks in every state the screens show — delivered to a pull
request, asking its owner a question, running (to steer and watch its live
diff), with CI failing, changes requested, approved and ready, merged,
conflicting — then prints where to open it and a key for each person, and
stays up until Ctrl-C, when it removes what it made.

    cd tests && uv run python demo.py            # builds first
    cd tests && uv run python demo.py --port 5180 --no-build

Sign in as one person in one browser and another in a private window to see
two people at once: presence, waiting on you, who steered, who merged.
"""

from __future__ import annotations

import argparse
import json
import signal
import sys
import time
from pathlib import Path

import requests

from build import build, build_gallery, build_web
from env import TestEnvironment
from fake_github import FakeGitHub
from helpers import ApiClient, create_api_key, create_organization, execute, wait_until, webhook_secret

SCRIPTED = {role: "fake/scripted" for role in ("implementer", "reviewer", "fixer", "simplifier")}

PEOPLE = [
    ("Ana Costa", "ana@demo.test", "admin"),
    ("Ben Okafor", "ben@demo.test", "member"),
    ("Chloé Martin", "chloe@demo.test", "member"),
    ("Dev Patel", "dev@demo.test", "member"),
]


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--port", type=int, default=5180, help="where the web app listens (default 5180)")
    ap.add_argument("--no-build", action="store_true", help="skip building the orchestrator, gallery and web app")
    args = ap.parse_args()

    if not args.no_build:
        build()
        build_gallery(force=True)
        build_web()

    # A web app left on the port from another run would proxy to that run's
    # backend and answer every call with an error: refuse rather than serve it.
    import socket
    with socket.socket() as probe:
        if probe.connect_ex(("127.0.0.1", args.port)) == 0:
            sys.exit(f"port {args.port} is in use (another demo?): stop it, or pass --port")

    env = TestEnvironment()
    env.web_port = args.port

    def stop(*_: object) -> None:
        print("\nstopping…", flush=True)
        raise SystemExit(0)

    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)

    try:
        run(env, args)
    except BaseException:
        if gh_ref:
            gh_ref[0].stop()
        env.teardown()
        raise


gh_ref: list = []


def seed_forge_credential(client: ApiClient, gh: FakeGitHub) -> requests.Response:
    response = client.post(
        "/v1/forge/credential",
        {"auth": "pat", "secret": "fake-token", "apiBaseUrl": gh.api_url},
    )
    response.raise_for_status()
    return response


def run(env: TestEnvironment, args) -> None:
    env.setup()
    if not env.wait_healthy(60):
        print(f"dude did not come up; logs in {env.log_dir}", file=sys.stderr)
        env.teardown(keep=True)
        sys.exit(1)
    env.start_web()

    org = create_organization(env.owner_dsn, "Acme")
    # Its seeded tiers play the scripted agent: every role is on one of them.
    execute(env.owner_dsn, "UPDATE model_tiers SET model = 'fake/scripted' WHERE organization_id = %s", (org,))
    # The first admin is provisioned, as a deployment would; the rest are invited through the API.
    admin_key = create_api_key(env.owner_dsn, org, name=PEOPLE[0][0])
    ana = ApiClient(env.control_plane_url, admin_key)
    ana.patch("/v1/me", {"name": PEOPLE[0][0]})
    keys = {PEOPLE[0][0]: admin_key}
    clients = {PEOPLE[0][0]: ana}
    ids = {PEOPLE[0][0]: ana.get("/v1/me").json()["person"]["id"]}
    for name, email, role in PEOPLE[1:]:
        r = ana.post("/v1/people", {"name": name, "email": email, "role": role})
        r.raise_for_status()
        body = r.json()
        keys[name] = body["key"]
        clients[name] = ApiClient(env.control_plane_url, body["key"])
        ids[name] = body["person"]["id"]
    ben, chloe = clients["Ben Okafor"], clients["Chloé Martin"]
    # Faces: two photos (uploaded, so kept in the demo's bucket) beside two
    # people with initials.
    photos = Path(__file__).resolve().parent.parent / "docs" / "design" / "mockups" / "photos"
    for name, photo in (("Ana Costa", "ana.jpg"), ("Chloé Martin", "cy.jpg")):
        requests.put(env.control_plane_url + "/v1/me/photo", data=(photos / photo).read_bytes(), timeout=30,
                     headers={"authorization": f"Bearer {keys[name]}", "content-type": "image/jpeg"}).raise_for_status()

    gh = FakeGitHub(env.git_root, owner="acme", repo="dashboard")
    gh.start()
    gh_ref.append(gh)
    r = seed_forge_credential(ana, gh)
    gh.webhook_url = env.control_plane_url + r.json()["webhookPath"]
    gh.webhook_secret = webhook_secret(env.owner_dsn, org)

    dash = ana.create_project(name="Dashboard", slug="dashboard", runtimeImage="dude-runtime:test", agentModels=ana.on_models(SCRIPTED),
                              repositories=[{"name": "dashboard", "url": gh.clone_url, "defaultBranch": "main"}])
    billing = ana.create_project(name="Billing API", slug="billing", agentModels=ana.on_models(SCRIPTED))
    # Agents that stay put, each in a project whose implementer is set for it
    # from the start (a phase takes its project's models as it begins).
    gh_live = gh.add_repository("insights")
    insights = ana.create_project(name="Insights", slug="insights", runtimeImage="dude-runtime:test",
                                  agentModels=ana.on_models({**SCRIPTED, "implementer": "fake/live"}),
                                  repositories=[{"name": "insights", "url": gh_live.clone_url, "defaultBranch": "main"}])
    pid = dash["id"]
    charts = ana.post(f"/v1/projects/{pid}/epics", {"title": "Charts v2", "description": "Replace the hand-rolled SVG charts with one library."}).json()
    l10n = ana.post(f"/v1/projects/{pid}/epics", {"title": "Localisation", "description": "English, Portuguese and German on every screen."}).json()
    exports = ana.post(f"/v1/projects/{pid}/epics", {"title": "Exports", "description": "PNG, CSV and PDF for every chart."}).json()
    ana.patch(f"/v1/epics/{exports['id']}", {"state": "planned"})

    def task(client: ApiClient, title: str, epic: dict | None, people: list[str], goal: str = "", project: str = "") -> dict:
        goal = goal or f"{title}, so the dashboard reads right for every customer who opens it."
        t = client.create_task(project or pid, title, epicId=epic["id"] if epic else None, goal=goal,
                               acceptanceCriteria=["It works", "It has a test"])
        client.put(f"/v1/tasks/{t['id']}/people", {"people": [ids[p] for p in people]})
        return t

    def deliver(client: ApiClient, t: dict) -> None:
        client.post(f"/v1/tasks/{t['id']}/deliver").raise_for_status()

    def pr_of(client: ApiClient, t: dict, timeout: float = 300) -> dict:
        return wait_until(lambda: (p := client.get("/v1/pull-requests", params={"taskId": t["id"]}).json()["pullRequests"]) and p[0],
                          timeout=timeout, message=f"{t['title']}: no pull request")

    print("seeding: deliveries run through the scripted agents, a minute or two…", flush=True)
    delivered = [
        (task(ana, "Pick the chart library and port the revenue chart", charts, ["Ana Costa", "Chloé Martin"]), ana, "ci_red"),
        (task(ben, "Area chart for weekly cost", charts, ["Ben Okafor"]), ben, "changes"),
        (task(chloe, "Legend toggles series", charts, ["Chloé Martin"]), chloe, "ready"),
        (task(ben, "Remove the hand-rolled SVG helpers", charts, ["Ben Okafor", "Ana Costa"]), ben, "merged"),
        (task(ana, "Axis labels respect the locale", l10n, ["Ana Costa"]), ana, "conflict"),
    ]
    for t, client, _ in delivered:
        deliver(client, t)
    # Their pull requests first: a task takes the project's models as it
    # starts, so the one-off models below wait until these are past it.
    prs = [pr_of(client, t) for t, client, _ in delivered]
    # Asking its owner: the project's implementer asks, just for this one.
    ana.patch(f"/v1/projects/{pid}", {"agentModels": ana.on_models({**SCRIPTED, "implementer": "fake/ask"})})
    asking = task(ana, "Localise the settings screens", l10n, ["Ana Costa"])
    deliver(ana, asking)
    wait_until(lambda: ana.get("/v1/questions", params={"taskId": asking["id"]}).json().get("questions"),
               timeout=120, message="the question never came")
    ana.patch(f"/v1/projects/{pid}", {"agentModels": ana.on_models(SCRIPTED)})
    live = task(ben, "Move data loading into useDashboardData", None, ["Ben Okafor", "Dev Patel"], project=insights["id"])
    deliver(ben, live)
    task(chloe, "Revenue chart zoom", charts, ["Chloé Martin"])
    task(ana, "PNG export", exports, ["Ana Costa"])
    ben.create_task(billing["id"], "Invoices in euros", goal="Customers billed in the EU see their invoices in euros.")

    for (t, client, state), pr in zip(delivered, prs):
        n = pr["number"]
        for check in ("unit", "lint", "typecheck"):
            gh.set_check(n, check)
        if state == "ci_red":
            gh.set_check(n, "e2e (chrome)", conclusion="failure")
            gh.review(n, "CHANGES_REQUESTED", "Import only the visx packages you use.", reviewer="chloe-m")
        elif state == "changes":
            gh.set_check(n, "e2e (chrome)")
            gh.review(n, "CHANGES_REQUESTED", "Use formatUsd for the tooltip.", reviewer="ana-costa")
            gh.set_unresolved(n, 2)
        elif state == "ready":
            gh.set_check(n, "e2e (chrome)")
            gh.approve(n, reviewer="ben-okafor")
        elif state == "merged":
            gh.set_check(n, "e2e (chrome)")
            gh.approve(n, reviewer="chloe-m")
            gh.merge(n)
        elif state == "conflict":
            gh.set_check(n, "e2e (chrome)")
            gh.approve(n, reviewer="ben-okafor")
            gh.set_conflicting(n)

    # Presence: the others were somewhere a moment ago.
    for name, where in (("Ben Okafor", "Charts v2"), ("Chloé Martin", "DASH-3")):
        execute(env.owner_dsn, "UPDATE people SET last_seen_at = now(), last_seen_where = %s WHERE id = %s", (where, ids[name]))

    info = {
        "open": env.web_url,
        "sign in as (paste the key)": keys,
        "backend": env.control_plane_url,
        "logs": str(env.log_dir),
        "database": env.db_name,
    }
    print(json.dumps(info, indent=2, ensure_ascii=False), flush=True)
    print("\nUp. Ctrl-C to stop and remove it.", flush=True)
    while True:
        time.sleep(3600)


if __name__ == "__main__":
    main()
