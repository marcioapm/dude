---
title: Judgements
covers: [settings:judgements]
---
Some of dude's decisions are better made by reading than by rule. For those, dude asks one model one question. This page explains what judgements are, where dude uses them, and how to choose or turn off the tier that answers.

## What judgements are

A **judgement** is one call to a model, with no agent and no tools. dude asks one question and reads back **yes**, **no** or **unsure**, with how sure the model is and why.

Judgements **fail closed**. When judgements are off or have no model, the caller skips that check. When they are on, an error, a malformed answer or **unsure** does not count as a pass: a phase's checks are marked unverified and sent back, or escalated after their rounds. A judgement never makes dude take a step it would not take otherwise.

Every call is recorded: what was asked, what the model answered, how long it took and how many tokens it used.

## Where dude uses them

dude uses a judgement to check an agent's **checks report**. The implementer, fixer and simplifier end their work with the checks they ran. dude asks the judgement tier whether each reported pass is backed by the commands the Run actually recorded, and whether any check is missing. For a check the agent says failed, it asks whether the Run's commands show the same failure at the commit the Run started from; for one not run, whether a later phase of the delivery runs it.

- A check the model does not confirm is **unverified**, and the work is sent back. See [Delivering a task](help:delivery#the-checks-report).
- With judgements off, dude publishes the work and says visibly that its checks were not model-checked. A check the agent says failed or did not run is still sent back.

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
