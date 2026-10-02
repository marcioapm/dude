# Model tiers

Every agent session runs on a model tier, never a model. A tier names the
one model dude requests from the LLM proxy for it. This is how tiers are
kept, chosen, sent to the agent and shown.

## The model

- **Tiers are the organisation's.** Each is `{id, name, description,
  model, position}` with who changed it and when (`model_tiers`, migration
  068). Everyone in the organisation reads them; only its admins add,
  change, reorder and remove them. There is no tier of a project's own,
  and no project or role names a model instead.
- **Bounds.** A name is unique in its organisation whatever its case, at
  most 24 characters; what it is for, at most 80. `model` is the name the
  proxy knows the model by, sent as-is on every call (`claude-opus-5-5`,
  `gpt-5.6-sol`): no provider prefix, no whitespace or `/`, at most 200
  characters — except the scripted agent's test models (`fake/scripted`,
  `fake/hang`, …). `modelTierInputSchema` and the table's CHECKs hold the
  same bounds. `model` may be null: not set yet.
- **Seeded.** Every organisation starts with **Thinker** ("Reads, plans,
  judges and tidies. Slow and thorough."), **Coder** ("Writes and fixes
  code for hours at a time.") and **Fast** ("Small, mechanical jobs where
  speed beats depth."), naming no model, and its roles on them: Thinker for
  the investigator, reviewer, simplifier and tester (and the orchestrator,
  if it has settings), Coder for the implementer — the fixer follows it —
  and Fast for none. New organisations get them from a trigger on
  `organizations`.
- **What dude does not know.** Which model, or which provider, the proxy
  actually served: it may fall back by its own config. Nothing in dude says
  "falls back to"; a session shows what dude requested.

## Who runs on what

- **Every role names a tier**: `tier` in a role's settings, on the same two
  layers as its effort, time limit and machine — the organisation's
  `default_agent_models[role]` and a project's `agent_models[role]` —
  resolved a field at a time. The first layer that names a tier decides;
  the fixer follows the implementer where it names none
  (`resolveTier`, and `delivery.ResolveRole` for the Run). The settings
  response carries it as a `Setting` with its source; on a project also
  what the organisation's layer names (what Reset goes back to), and for
  the fixer whether it is the implementer's.
- `model` is gone from role settings. A request that still sends one is
  refused (400, "a role names a model tier (`tier`, one of the
  organization's tiers), not a model"); one naming a tier the organisation
  lacks is refused too.
- **Reasoning effort stays per role**, sent as before. The proxy drops it
  for models it knows do not take it.
- **Editing a tier's model** moves every agent on it from its next session;
  a running session finishes on the model it started with (a resume builds
  its spec from what its Run recorded).
- **Removing a tier in use** takes a replacement tier and, in one
  transaction, rewrites every organisation and project `agent_models[*].tier`
  that named it, then deletes it. A settings change naming a tier holds that
  row `FOR SHARE`; the removal locks it `FOR UPDATE`, so nothing is left
  naming a tier that is gone. Removing one nothing uses needs no
  replacement; removing the last tier is refused (409).

## How a tier reaches the agent

- When a phase Run's spec is built, the orchestrator resolves role → tier
  id → the tier's row → its model (`delivery.TierFor`). A role on no tier,
  on a tier that is gone, or on one naming no model fails the Run without
  reaching lux, the reason in words: "The Reviewer runs on Thinker, which
  names no model yet. An admin sets it in Models."
- **The orchestrator declares the model itself.** `OPENCODE_CONFIG_CONTENT`
  is `{"model": "<provider>/<model>", "provider": {"<provider>": {"models":
  {"<model>": {}}}}, "agent": {"build": {"reasoningEffort": …}}}`, the
  provider by the model's name (`llm.Provider`, the one rule): `claude-*`
  through `llm-anthropic` (Anthropic Messages), anything else through
  `llm-openai` (OpenAI-compatible Chat Completions). The image keeps its two
  providers and their URL and key from `DUDE_LLM_URL` / `DUDE_LLM_KEY`.
- Why the declaration: OpenCode (1.18.34 in the image) refuses a model its
  provider does not declare (`ProviderModelNotFoundError`), and deep-merges
  the inline config over the image's file (remeda `mergeDeep`, inline
  last). So a model the image's `opencode.json` lists keeps its `limit` and
  `reasoning`, and any other name the proxy serves works with no image
  change, with OpenCode's defaults: no context limit (no automatic
  compaction) and 32000 output tokens a request.
- Test-harness models (`fake/…`) take the scripted agent's path as before.
- **Each Run records what it requested** in the statement that records its
  lux Run id: `runs.model` (the model) and `runs.model_tier` (the tier's
  name then). The spec's labels say the same (`dude.model`,
  `dude.model_tier`). A turn that fails before the agent did anything names
  the tier and the model it requested.

## The proxy, from the orchestrator

The orchestrator holds the proxy's key; the backend never does.

- `GET /internal/llm/models` (served as `GET /v1/models/proxy`) reads the
  proxy's `GET /models` for the Models page's suggestions. Unreadable is no
  models and why; a tier still takes any name.
- `POST /internal/llm/test` (served as `POST /v1/models/test`, admins only)
  sends one tiny request (16 tokens, bounded to 30 s) for a model per
  distinct request the tier's agents would send — once with none for a tier
  nobody uses — in the wire format the agent would use: Anthropic Messages
  for a Claude model (with no effort: OpenCode's Anthropic provider drops
  `reasoningEffort`), Chat Completions with `reasoning_effort` (max as
  high) otherwise. Efforts that go out alike are one request: high and max
  for an OpenAI model, any effort for a Claude one. Each result names the
  efforts it covers and what was sent, and is its latency, or the proxy's
  status and error message as it sent them. It is a check, never a gate.

## Upgrade

Migration 068 gave the organisations there before it the three tiers. Each
tier asks for the model most of its roles already named, the image
provider's prefix taken off (a tie goes to the first role in its order;
none, no model). Every organisation role names its tier and no model. A
project override with a model points at the tier that already asks for
it, else at a tier made for it (once per model per organisation, named
after the model). `model_tier_upgrade_notes` keeps, for each organisation
role and project override that named a model, the old model, the tier it
asks for now and whether what it requests changed; the Models page shows
them to admins until one dismisses them.

## Screens

- **Organisation › Models** (between Agents and Machines, with the count of
  tiers): what a tier is, the three steps (an agent asks for a tier, dude
  requests its model, the proxy serves it), and the tiers table — mark,
  name and what it is for, the model it requests ("Not set" in the
  attention tone), who changed it, who uses it, and a row menu: Change
  model…, Send a test message, Remove…. A member sees it read-only.
- **Add / change a tier**: name, what it is for, the model to request with
  the proxy's names as chips, labelled as suggestions. A name the proxy
  does not list shows the attention callout and the button reads "Save
  anyway" / "Add tier anyway". Send a test message shows one answer per
  request sent, naming the efforts it covers (and what was sent, when that
  differs) and who uses the tier at them.
- **Remove**: who uses the tier, "Move them to", "Remove and move them".
- **Agents › each role**: Model is a tier picker (on a project, "From Acme ·
  Thinker" first). Under its list an admin is offered "Manage tiers in
  Models"; anyone else reads "Tiers are Acme's — ask an admin to change
  one" on a project, "Only admins change tiers." on the organisation.
  On the organisation, "Requests <model>" under it; on a project, its
  source and Reset. Reasoning effort says the proxy drops it for models
  that do not reason.
- **Session header**: a `TierChip`, "Coder · claude-opus-5-5", whose
  tooltip says that is what dude requested when the session started, and
  that changing the tier changes the next session. A Run from before tiers
  shows its model alone.

The design system pieces (TierLine, FlowSteps, NameChips, TierChip) are
in its README.
