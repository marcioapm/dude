---
title: Judgements
covers: [settings:judgements]
---
Some of dude's decisions are better made by reading than by rule. For those, dude asks one model one question. This page explains what judgements are, where dude uses them, and how to choose or turn off the tier that answers.

## What judgements are

A **judgement** is one call to a model, with no agent and no tools. dude asks one question and reads back a structured answer, with how sure the model is and why.

Judgements **fail closed**. When judgements are off or have no model, the caller does what it does without one. An error or a malformed answer is never acted on. A judgement never makes dude take a step it would not take otherwise.

Every call is recorded: what was asked, what the model answered, how long it took and how many tokens it used.

## Where dude uses them

dude uses a judgement to **route a chat message**: when someone mentions dude in a connected chat, the judgement tier decides which task or project the message is about, and, for a mention that starts a session, which projects it could need. See [Brainstorm sessions](help:brainstorm).

The judgement tier does not check an agent's **checks report**: the task's conductor decides it, and with no conductor deciding the delivery, a check the agent says failed or did not run is sent back for a person. See [Delivering a task](help:delivery#the-checks-report).

## Choosing the tier

Pick the tier in **Settings → Judgements → Who answers → Tier**. Questions are short, so a fast tier is usually right. Judgements use the organisation's model tiers; see [Models and tiers](help:models-and-tiers#what-a-tier-is).

The menu marks Judgements "no model" when the chosen tier names no model. Then no judgement is made.

Every member sees this page. Only an organisation admin changes the tier or sends a test question.

## Turning judgements off

Choose **Off — nothing is judged**. Every caller falls back to what it does without one. Judgements are off by default, for a new organisation.

## Recent judgements

**Recent judgements** lists every call, newest first, filtered by who asked. Open one to see its tier, model, time taken, tokens, the question and the answer.

## What agents can read

Agents read the judgement setting with `settings`, section `judgements`: Off, or the chosen tier and its model. They never see calls or tests.
