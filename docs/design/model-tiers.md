# Model tiers

Every agent session runs on a model tier, never a model. A tier names the
one model dude requests from the LLM proxy for it, and how hard that model
thinks. This is how tiers are kept, chosen, sent to the agent and shown.

## The model

- **Tiers are the organisation's.** Each is `{id, name, description,
  model, effort, options, headers, position}` with who changed it and when
  (`model_tiers`, migrations 069 and 096). Everyone in the organisation
  reads them; only its admins add, change, reorder and remove them. There
  is no tier of a project's own, and no project or role names a model or
  an effort instead.
- **A tier is a variant.** "Thinker (High)" and "Thinker (Medium)" are two
  tiers on one model with different efforts. Settings live in dude, not in
  the proxy, so a new model setting is a tier edit, not a release.
- **Bounds.** A name is unique in its organisation whatever its case, at
  most 24 characters; what it is for, at most 80. `model` is the name the
  proxy knows the model by, sent as-is on every call (`claude-opus-5-5`,
  `gpt-5.6-sol`): no provider prefix, no whitespace or `/`, at most 200
  characters — except the scripted agent's test models (`fake/scripted`,
  `fake/hang`, …). `model` may be null: not set yet.
  - `effort`: `none`, `low`, `medium`, `high` or `max`; null is the model's
    default. On Claude models, None turns thinking off. GPT models reason
    at their default: `none` sends them no `reasoningEffort`.
  - `options`: extra OpenCode model options, a JSON object merged over what
    the effort makes (the tier's keys win), at most 4 KB as Postgres renders
    it. The AI SDK drops keys it does not know without a word.
  - `headers`: extra request headers, `{name: value}`, names in RFC 9110
    token syntax, values one line, at most 4 KB.

  `modelTierInputSchema` and the table's CHECKs hold the same bounds.
- **Seeded.** Every organisation starts with **Thinker** ("Reads, plans,
  judges and tidies. Slow and thorough.") at `high`, **Coder** ("Writes and
  fixes code for hours at a time.") at `medium` and **Fast** ("Small,
  mechanical jobs where speed beats depth.") at the model's default, naming
  no model, and its roles on them: Thinker for the conductor, brainstorm,
  investigator, reviewer, simplifier and tester, Coder for the implementer
  — the fixer follows it — and Fast for none. New organisations get them
  from a trigger on `organizations`. Tiers from before migration 096 keep
  the model's default.
- **What dude does not know.** Which model, or which provider, the proxy
  actually served: it may fall back by its own config. Nothing in dude says
  "falls back to"; a session shows what dude requested.

## Who runs on what

- **Every role names a tier**: `tier` in a role's settings, on the same two
  layers as its time limit and machine — the organisation's
  `default_agent_models[role]` and a project's `agent_models[role]` —
  resolved a field at a time. The first layer that names a tier decides;
  the fixer follows the implementer where it names none
  (`resolveTier`, and `delivery.ResolveRole` for the Run). The settings
  response carries it as a `Setting` with its source; on a project also
  what the organisation's layer names (what Reset goes back to), and for
  the fixer whether it is the implementer's.
- `model` and `effort` are gone from role settings. A request that still
  sends one is refused (400, "a role names a model tier …" / "reasoning
  effort is the model tier's (set it in Models), not the role's"); one
  naming a tier the organisation lacks is refused too. Migration 096
  deleted every role's `effort`.
- **Editing a tier** moves every agent on it from its next session; a
  running session finishes on the model and settings it started with (lux
  keeps its spec's env; a resume rebuilds its labels from what its Run
  recorded).
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
  {"<model>": {"options": …, "headers": …}}}}}`, the provider by the
  model's name (`llm.Provider`, the one rule): `claude-*` through
  `llm-anthropic` (`@ai-sdk/anthropic`, the Messages API), anything else
  through `llm-openai` (`@ai-sdk/openai`, the Responses API). The image
  keeps its two providers and their URL and key from `DUDE_LLM_URL` /
  `DUDE_LLM_KEY`.
- **A tier's effort is model options** (`llm.ModelOptions`, the only
  provider knowledge beside `llm.Provider`). Model-level options go on
  every request; an agent's `variant` does not take effect.

  | Effort | `llm-anthropic` | `llm-openai` |
  | --- | --- | --- |
  | null | `thinking: {type: adaptive, display: summarized}` | `reasoningSummary: auto` |
  | `none` | `thinking: {type: disabled}` | `reasoningSummary: auto` |
  | other | the adaptive `thinking`, and `effort: <effort>` | `reasoningSummary: auto`, `reasoningEffort: <effort>` |

  Claude 5.x returns thinking blocks with empty text unless asked for a
  summarized display, which is why the chat never showed a thought before;
  the provider sends `effort` as `output_config.effort`. The Responses API
  returns reasoning text only as a summary, and takes `max` as it is. The
  tier's `options` are then deep-merged over that, the tier's keys
  winning, and its `headers` become the model's `headers`. gpt-6-sol
  refuses `none` and `minimal` (an error event inside a 200 stream).
- Why the declaration: OpenCode (1.18.34 in the image) refuses a model its
  provider does not declare (`ProviderModelNotFoundError`), and deep-merges
  the inline config over the image's file (remeda `mergeDeep`, inline
  last). So a model the image's `opencode.json` lists keeps its `limit` and
  `reasoning`, and any other name the proxy serves works with no image
  change, with OpenCode's defaults: no context limit (no automatic
  compaction) and 32000 output tokens a request.
- Test-harness models (`fake/…`) take the scripted agent's path as before.
- **Each Run records what it requested** in the statement that records its
  lux Run id: `runs.model` (the model), `runs.model_tier` (the tier's name
  then) and `runs.effort` (its effort then). The spec's labels say the
  same (`dude.model`, `dude.model_tier`, `dude.effort`). A turn that fails
  before the agent did anything names the tier and the model it requested.

## The proxy, from the orchestrator

The orchestrator holds the proxy's key; the backend never does.

- `GET /internal/llm/models` (served as `GET /v1/models/proxy`) reads the
  proxy's `GET /models` for the Models page's suggestions. Unreadable is no
  models and why; a tier still takes any name.
- `POST /internal/llm/test` (served as `POST /v1/models/test`, admins only)
  sends one tiny request (16 tokens, bounded to 30 s) for the tier's model,
  effort, options and headers as the dialog has them, as its agent would:
  Anthropic Messages with the tier's `thinking` and `output_config` for a
  Claude model, else a streamed `/v1/responses` with its `reasoning` (the
  proxy takes Responses only streamed, with `store: false`). The result is
  what was sent, and its latency, or the proxy's status and error message
  as it sent them — a refusal inside a 200 stream included. It is a check,
  never a gate.

## Upgrade

Migration 069 gave the organisations there before it the three tiers. Each
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
  attention tone), its effort ("Model's default" when null), who changed
  it, who uses it, and a row menu: Change model…, Send a test message,
  Remove…. A member sees it read-only.
- **Add / change a tier**: name, what it is for, the model to request with
  the proxy's names as chips, labelled as suggestions, and **Reasoning
  effort** (Model's default, None, Low, Medium, High, Max; "On Claude
  models, None turns thinking off. GPT models reason at their default."). An **Advanced** disclosure holds
  two JSON fields, "OpenCode model options" and "Request headers", checked
  in the dialog by the same rules as the API. A name the proxy does not
  list shows the attention callout and the button reads "Save anyway" /
  "Add tier anyway". Send a test message sends the dialog's settings once
  and shows the answer and what was sent.
- **Remove**: who uses the tier, "Move them to", "Remove and move them".
- **Agents › each role**: Model is a tier picker (on a project, "From Acme ·
  Thinker" first). Under its list an admin is offered "Manage tiers in
  Models"; anyone else reads "Tiers are Acme's — ask an admin to change
  one" on a project, "Only admins change tiers." on the organisation.
  On the organisation, "Requests <model> · <effort>" under it; on a
  project, its source and Reset. A role has no effort field.
- **Session header**: a `TierChip`, "Coder · claude-sonnet-5 · medium" (the
  effort only when the Run asked for one), whose tooltip says that is what
  dude requested when the session started, and that changing the tier
  changes the next session. A Run from before tiers shows its model alone.

The design system pieces (TierLine, FlowSteps, NameChips, TierChip) are
in its README.
