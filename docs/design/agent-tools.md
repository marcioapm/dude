# Tools for agents: dude's MCP server

Status: design, 2026-09-24. Task #44 (and #45, repository access, built on
it).

## What

Agents get tools that act on dude itself — the things today only a person
can do in the UI — through an MCP server the orchestrator serves (MCP
streamable HTTP, the official Go SDK `github.com/modelcontextprotocol/go-sdk`).
lux passes it to every agent session (`workload.mcpServers`, agreed with lux;
ACP `mcpServers` for OpenCode, `--mcp-config` for Claude Code).

## Tools (first set)

| tool | what it does | who may |
|---|---|---|
| `create_work_item` | a new work item in the same project (title, goal, criteria, epic, repositories) — not delivered; a person decides | implementer, investigator |
| `create_epic` | a new epic in the project | investigator |
| `list_work` | the project's epics and work items, with status and keys | all |
| `search_memory` | search the project's history: work items, findings, artifacts' text, PR titles (Postgres full-text first; embeddings later) | all |
| `ask_person` | a question for a person; the turn ends and the answer is the next input — the tool form of today's ```` ```question``` ```` block | implementer, fixer |
| `publish_artifact` | write a file for people (name, content) — same as `$LUX_ARTIFACTS`, for agents that prefer a tool | all |
| `request_repository` | ask for another repository of the organization, read or write, with a reason; a person approves or denies (#45) | all |

Everything an agent creates is marked as created by that Run (`created_by_run_id`)
and shown so in the UI; nothing an agent creates starts work on its own.

## Identity and authority

- Each phase Run gets its own **bearer token**, minted at submit, stored
  hashed on the Run, sent to lux as a secret (`headers: [{name:
  Authorization, secret: DUDE_MCP_TOKEN}]`), revoked when the Run ends.
  The token names the Run; the Run names the organization, project, work
  item and role. A tool call can do nothing outside that project.
- **Which tools a role gets** is policy (project settings, defaults above);
  the server lists only those to that Run.
- Every call is a ledger event (`agent.tool.dude`) with its arguments and
  result, so the chat shows it like any other tool call.

## Where it runs

In the orchestrator process, on its own listener (`DUDE_MCP_LISTEN`), since
it acts on the same data and workflows. lux never lets a Run reach the lux
host (loopback, the host's addresses, the control plane's address), so the
listener must be reachable from lux's hosts some other way: in production
its own host name; locally, the machine's LAN address or a container on
lux's network. The egress rule for it is added to every spec.

## Order of work

1. Server skeleton: token minting and auth, `list_work`, `create_work_item`,
   the ledger event; Go tests calling it as an MCP client.
2. Spec: `workload.mcpServers` + the token secret + egress (behind a config
   flag until lux lands it; fake lux accepts and records it).
3. `search_memory`, `publish_artifact`, `ask_person`, `create_epic`.
4. `request_repository` + approval UI + resume with added repositories (#45,
   needs lux's add-repositories-on-resume).
5. Contract test on real lux once `mcpServers` lands: lux-fake calls a dude
   tool, the call appears in the chat and its effect in the API.
