---
title: Memory
covers: [settings:memory, tool:search_memory, tool:get_memory, tool:remember]
---
Memory is what people, dude and agents chose to remember, and one search over it and the work. This page covers the kinds of memory, how search works, the index, and who can change what.

## What memory is

A **memory** is a short piece of knowledge worth having next time: a fact that holds ("the billing API paginates by cursor"), a procedure that works, a trap and its way around it. Each has a title, Markdown content and a kind:

- **fact** (the default) — something that is true;
- **procedure** — steps that work;
- **note** — anything else worth keeping.

A memory can say which tasks or epics it is about. It is live as soon as it is saved, and marked with who wrote it: a person, dude, or an agent.

## Where a memory applies

- A **project** memory is found in that project only.
- An **organisation** memory is found in every project.
- A **session** memory, saved by a brainstorm agent, is found only by that session's members and its agent.

## Searching

`search_memory` searches memories together with the project's tasks, epics and the project itself, best first. It searches two ways at once and combines them:

- **by words** — the terms in your query;
- **by meaning** — what your query is about, even in other words, when embeddings are available.

You can limit a search to some types: memory, task, epic or project. dude's own guide — these pages — is searchable too, as type `doc`, so an agent can answer a question about dude from the guide and link you to the page.

Agents are told to search before they investigate something that may already be known, and before they save something new. `get_memory` reads one memory in full.

## Saving a memory

Agents save memories with `remember`. They are told to search first so they do not save the same thing twice, and not to save what the code or the task already says.

People add memories in **Settings → Memory → Memories**.

## The Memory pages

**Settings → Memory** has three pages, on the organisation and on each project:

- **Search** — organisation and project memories, tasks, epics and projects, ranked by words and meaning. **As the agent sees it** shows these results in the agent's format. Agents also search dude's built-in guide sections, which are not shown here.
- **Memories** — what people, dude and agents chose to remember. Archive one to take it out of every search; restore it to bring it back.
- **Index** — the organisation's indexed memories and work, and their embeddings for search by meaning. Built-in guide sections and their embeddings are not listed or managed on this page.

## The index

Everything searchable is indexed by its words at once. Its meaning is added when an embedding is made for it. Until then, it is still found by its words.

If the embedding service refuses a document, dude tries again later: after a minute, then 5 minutes, 30 minutes, 2 hours, and then daily. It never gives up. The **Index** page lists failures, with **Retry** for one or all.

Embeddings are configured by whoever runs dude, not in organisation settings. Without them, the Index page says memory is searched by words alone.

Agents can read the embedding configuration with `settings`, section `memory`. It shows no memory contents.

## Who can change what

- Every member can search and add memories.
- You can change and archive your own memories. An organisation admin can change and archive anyone's, including agents' and dude's.
- Only an organisation admin can rebuild the index.
