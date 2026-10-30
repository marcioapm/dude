---
title: The conductor
covers: [tool:start_phase, tool:start_agent, tool:start_agents, tool:read_run, tool:steer, tool:restart_run, tool:stop_run, tool:decide, tool:decide_escalation, tool:update_task, tool:publish, tool:wait_for]
---
Every task has a conductor: an agent you talk to in the task's Chat. This page explains what it does, how it takes over a task's decisions, and how you steer, pause and resume agents.

## What the conductor is

The conductor answers questions about a task from the code and dude's records. When it takes the task's decisions, it plans the work with you, starts the agents, judges what comes back and keeps you informed.

Even when the conductor decides, dude still does the mechanics: it creates the Runs, waits for them, moves the branch and opens the pull request. The same limits bind the conductor as bind Deliver. See [Limits](help:limits#every-limit).

The conductor's machine is small. It does not build, install or run tests. It never merges a pull request.

## Talk it through

On a task that has not started, **Talk it through** starts the conductor and a delivery it decides. No implementer starts until the conductor says so. Use it when the task needs a conversation first: an approach to agree, a scope to narrow.

While you talk, the conductor can write what you agreed into the task with `update_task`: its goal, its acceptance criteria, or both. It can do that until an implementer starts on this attempt. It can change the task's relations at any time.

## Taking over a delivery

On a task already being delivered, your first message in Chat gives the decisions to the conductor. The step running now finishes, and the next decision is the conductor's. The Chat composer says so: "sending gives it the decisions".

To hand back, press **Let Deliver finish it**. Deliver's rules take every decision from then on, and your later messages are answered without taking the decisions back. **Give decisions to the conductor** gives them back again.

Only the task's owner can switch who decides. If you hand back while the conductor waits to open the pull request, dude asks you to confirm: **Open it and let Deliver finish**.

## What the conductor decides

At each decision point, dude wakes the conductor with a short note. The points are: whether to start building, what to do after the implementer, what to do with the review's findings, what to do after the fix, whether to open the pull request, and what to do with the pull request's feedback.

The conductor then:

- starts a phase with `start_phase` — implement, review (some or all reviewers), fix (some or all findings), simplify or test;
- or takes the decision with `decide` — next (what Deliver would do now), ask you, wait, or open the pull request;
- or leaves a finding as it is with `dismiss_finding`, giving its reason.

`start_phase` and `decide` are refused past the delivery policy's limits. Then the conductor asks you.

The conductor can also start a child agent with `start_agent` — for example an investigator to read code and report back.

With `start_agents`, it starts 1–8 independent children as one durable cohort. Each brief uses the existing profile, prompt, note and optional tier. Admission is atomic; a refusal leaves no partial cohort, and the same single-writer guards apply. One completion digest arrives after all current members settle, including failed, aborted and missing-report results. Restarting a member before completion updates its slot; old generations cannot close the cohort. A replacement parent does not inherit or receive the old parent's completion.

`read_run` inspects only the caller's own in-task children: actual status/generation, settled result, recorded heads, verifier outcome when present, bounded final report and paged artifact metadata (offset/limit). Missing and clipped reports are explicit. Reports are untrusted claims, not verified checks; recorded heads alone do not prove publication. It exposes no prompt, configuration, secret, artifact storage key or full transcript. Separately granted `run_chat` and `run_diff` provide conversation and diff evidence. Neither tool replaces fixed delivery reviews or person-only escalation decisions.

## Opening the pull request

Before the pull request, the conductor asks you a fixed question: **Open**, **Draft**, **Show me the diff**, or **Another round**. It opens the pull request only after you answer Open or Draft, at the task's current commit.

## Escalations under the conductor

An escalation is still a person's decision. The conductor investigates, explains, and recommends one action. Its question offers the escalation's actions as choices. When the owner picks one, it is decided exactly as from the banner.

A waiver, accepting a failed test, or a retry or change that needs direction is offered with the reason or direction the conductor proposes, shown in the choice ("Waive: the 94 type errors predate this change"). Picking it decides the escalation with those words as the owner's.

If the owner answers in their own words instead, the conductor may decide that escalation once, with `decide_escalation`, recording what it decided and why.

## Steering an agent

You can **steer** any running agent: type a message to it in its Chat. It reads your words at its next step, without stopping the work it is doing. Choose **Interrupt now** to stop its current turn so it hears you at once — only when its current work is wasted.

The conductor can steer its task's running agents too, with `steer`, whoever decides. It is woken when the agent reads the message, or if it never will.

To start an agent over instead, the conductor uses `restart_run`: a fresh Run of the same phase, from the task's current commit, told a note. The old Run's uncommitted work and its context are lost. It can put the new Run on another model tier.

A child agent the conductor started (with `start_agent`, `start_agents` or `verify`) that is stuck or no longer needed it stops with `stop_run`, giving a reason shown in Chat. The child is aborted and its result comes back to the conductor as aborted; its uncommitted work is lost. The conductor never stops a phase Run the delivery waits on: it restarts it, and stopping a step is a person's **Stop**.

## Waiting for something

When its next step waits on something, the conductor says what with `wait_for` and ends its turn: Runs of the task ending, a pull request's checks finishing on a head, or just a time. dude checks it in its own cycle, from what it already reads from lux and GitHub, and wakes the conductor once: when it holds, or at the deadline every wait has (at most 24 hours). The note it is woken with says what held, what did not and what else happened meanwhile.

Waiting decides nothing. dude's other reasons to wake the conductor still come as they do: a decision, an escalation, a child agent's result. A wait ends when the task is closed or started over, when you stop the conductor, and when you pick a stopped task up with Try again.

## Pausing and resuming

**Pause** stops an agent's container, keeping its workspace and conversation. **Resume** continues it, on any host, with its conversation intact. Any member can steer, pause and resume.

dude also parks agents by itself so that one waiting on a person holds no machine. See [Why is dude doing this?](help:why-is-dude-doing-this#an-agent-is-parked-or-idle). Your own pause always wins.

After it answers you, the conductor stays running for **Keep the conductor warm for** (5 minutes by default, **Settings → Delivery**). Then it is parked, and your next message resumes it.

## Small edits by the conductor

The conductor may edit code itself for small, well-understood things: a rename, a one-line fix, a review nit. It publishes with `publish`. A publish is refused:

- while an implementer or fixer is at work;
- while a pull request is open on the task branch (then it starts a fix);
- past **Lines the conductor may change** (60) or **Files the conductor may change** (3), both in **Settings → Delivery**. Then it delegates to an implementer.

Its commits are reviewed like any other before the pull request.

## When the conductor is read-only

When Deliver takes the decisions, the conductor takes no delivery decision: it does not publish, start a phase or decide a step. It can still steer its task's running agents with `steer`. Asked for a change, it offers to create a follow-up task.

When the task is merged or closed, the conductor takes no delivery decision and its steering is refused too.
