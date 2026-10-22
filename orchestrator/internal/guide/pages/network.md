---
title: Network
covers: [settings:network]
---
Agents run in containers that can reach only the hosts you allow. This page explains the allowlist, how a project adds to it, what is always reachable, and what to do when a tool cannot download something.

## What agents may reach

**Settings → Network → Agents may reach** is the organisation's list of hosts an agent may reach from inside its container: package registries, GitHub, an internal mirror. Everything else is refused.

An entry can be:

- a hostname, like `registry.npmjs.org`;
- an address or a range of addresses;
- a wildcard like `*.example.com`, which matches names under the domain but not the domain itself.

**Presets** add what a common toolchain needs, by name.

With an empty list, agents can reach only their model. `npm install`, `uv sync` and anything that asks GitHub will fail inside a Run.

## Always reachable

Every Run reaches its model's host and dude's own tools, whatever the list says. The page shows them under **Always reachable**.

Whoever runs dude can also set a list that applies under every organisation's. The page shows it in its note.

## Anywhere

**Anywhere** turns the list off: agents may reach any host. Use it for a dude on a private network, or while a toolchain is still being worked out. Code in a Run — and whatever a prompt talked it into — can then leave the container. The list is kept but not applied.

## Brainstorm sessions

**Brainstorm sessions** lets brainstorm agents reach any host, to read documentation, competitors' pages and anything else they find while they research. Every other role keeps the list.

Turning it on has a risk. A brainstorm has read-only checkouts of its session's repositories and the model's key in its container. A malicious page could talk it into sending them anywhere.

A change reaches brainstorm agents started after it.

## A project's network

On a project, **Settings → Network** shows the organisation's hosts, then the project's own on top. Add what the project needs.

Turn on **Only this project's list** to ignore the organisation's hosts for this project — for code that must stay tighter than the rest.

**Refused recently** lists hosts agents on the project tried in the last 7 days and were not allowed to reach. An admin can allow one from there.

Branch previews have their own **Egress allowlist** in the project's **Servers** settings.

## When a change applies

A change reaches Runs started after it. A running agent keeps the network it started with; the task's next Run gets the new list.

Only an organisation admin changes the network lists.

## What agents can read

Agents read the network settings with `settings`, section `network`: the hosts that apply, where each list is set, and which roles may search and read the web with their own tools.

Searching and reading the web with the coding agent's own tools are capabilities of their own, which every role has by default.
