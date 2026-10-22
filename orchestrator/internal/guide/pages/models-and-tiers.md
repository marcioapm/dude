---
title: Models and tiers
covers: [settings:models]
---
Agents never name a model. They run on a **tier**, and each tier names the model dude asks for. This page explains tiers, where each role's tier is set, and what happens when a tier names no model.

## What a tier is

A **tier** is the organisation's named choice of model — for example a fast tier and a strong one. Each tier has:

- a **name** (at most 24 characters) and a short description;
- the **model** dude requests from the LLM proxy, exactly as the proxy names it;
- an **effort** — how hard the model thinks: none, low, medium, high or max, or the model's default;
- optional extra options and headers sent with each request.

Change a tier's model in **Settings → Models**, and every agent on that tier uses the new model from its next session.

## Who can change tiers

Every member sees the tiers. Only an organisation admin adds, changes, tests or removes one. **Send a test message** checks that the proxy answers for a tier's model.

Removing a tier moves everything that named it — the organisation's roles, every project's overrides, the judgement tier — to the replacement you pick, in one step. Sessions already running on it keep their model.

## Where each role's tier is set

Each agent role names a tier in its **Model** field, in **Settings → Agents**:

- on the organisation, the default for every project;
- on a project, an override for that project, with **Reset** to follow the organisation again;
- the Fixer follows the Implementer's tier unless you give it its own.

A brainstorm session can choose its own tier when it starts; otherwise it follows the organisation's Brainstorm setting.

The conductor can restart a Run on another tier (`restart_run`), or start a child agent on one (`start_agent`). Those choices apply to that one Run.

## When a tier names no model

A role on no tier, or on a tier with no model, cannot start. Its Run fails and says why. The Models page marks a tier that "names no model yet". Pick a model for the tier, or move the role to another tier.

## What a Run records

Each Run records the tier, model and effort it requested, so you can see what an agent ran on. dude does not see whether the proxy served that model or a fallback from its own configuration.

## The judgement tier

One tier can also answer dude's one-question decisions. That choice is on its own page. See [Judgements](help:judgements#choosing-the-tier).

## What agents can read

Agents read the organisation's tiers with `settings`, section `models`: each tier's name, description, effort, order, requested model id, and whether that model is set. They never see a tier's options or headers. Section `roles` shows which tier each role is on, and where that is set.
