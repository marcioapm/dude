# Judgements

Some of dude's decisions are better made by reading than by rule: is this
pull-request comment something the fixer should act on, does this question
need a person. A **judgement** is dude asking one model one such question,
once, and reading back a decision: `yes`, `no` or `unsure`, a confidence
from 0 to 1, and a sentence or two of reasoning. No agent, no harness, no
tools: one HTTP request to the LLM proxy.

Nothing asks for a judgement yet. This is the capability, the setting and
the page; each caller is a change of its own, with its own threshold and
its own fallback.

## Two layers

1. **Calling a model directly** (`orchestrator/internal/llm`,
   `Client.Complete`). One OpenAI-compatible Chat Completions request,
   `POST {DUDE_LLM_URL}/chat/completions` with `Authorization: Bearer
   DUDE_LLM_KEY`, `stream: false`. The proxy translates it for every
   provider, so nothing here looks at the model's name; `llm.Provider` and
   `ModelOptions` stay for the OpenCode agent's config only. A `Call` names
   the model, the tier's effort (sent as `reasoning_effort`, `none`
   included; nothing for the model's default) and headers, the messages,
   and optionally `max_tokens` and `temperature`; it is bounded by its
   `Timeout` (60 s by default). The tier's OpenCode options are not
   applied: they are named for the agent's providers, not for Chat
   Completions. The `Completion` is the first choice's text (an array of
   parts joined), its finish reason, the model the proxy says served it,
   prompt and completion tokens, latency and HTTP status; a failed call
   returns what was learnt before it failed alongside its error.
   **Every call sent is recorded** in `model_calls` (`internal/modelcalls`).
2. **Judgements** (`orchestrator/internal/judge`, `Service.Judge`). An
   `Ask` is who asks (`asker`, as code names it), a short label of what
   about (`about`), an optional task, the question and its context. Judge
   reads the organisation's judgement tier; with none it returns `ErrOff`,
   with a tier naming no model `ErrNoModel` — in both cases nothing is sent
   and nothing recorded. Otherwise one `Complete` call: a fixed system
   message (`judge.System`) telling the model it decides one question for a
   software-delivery system and must answer only
   `{"decision":"yes"|"no"|"unsure","confidence":<0..1>,"reasoning":"…"}`,
   then the question with the context under `## Context`; `max_tokens`
   1024, temperature 0, the tier's effort and headers. No `response_format`:
   it is not carried across providers. The answer is read tolerantly: the
   first balanced `{…}` object (a Markdown fence and prose around it are
   skipped), valid only with a decision of yes, no or unsure and a
   confidence in [0, 1]; anything else is `ErrUnparseable`, the text kept.
   **It fails closed:** any error is a zero `Judgement` and the caller does
   what it does without one. The tier is read in one transaction and the
   call recorded in another; no transaction is held while the model thinks.
   The record is written under a 5 s bound of its own, detached from the
   caller's context, so a call sent is recorded even when the caller's
   deadline ends first.

## The setting

`organizations.judgement_tier_id` names one of the organisation's tiers;
NULL is **Off**, the default. Organisation only: no project overrides it.
Admins change it on **Organisation settings → Judgements**; everyone reads
it. The tier counts as a use of that tier: the Models page lists
"Judgements" under its Used by, and removing it needs a replacement, to
which the setting moves with the roles (`removeTier`, `ModelTierUse`'s
`{kind: "judgements"}`).

## The table

`model_calls` (migration 139), one row per call sent, under row-level
security like every tenant table:

| Column | |
| --- | --- |
| `id` | `mcl_…` |
| `purpose` | `judgement` (a CHECK list, for the next kind of direct call) |
| `asker`, `about` | who asked, and a short label of what about |
| `task_id` | the task it was about; SET NULL when the task goes |
| `tier_id`, `tier_name`, `model` | the tier (SET NULL when removed), and its name and model then |
| `request` | the body sent: messages and parameters. **Never the headers.** |
| `response_text` | the model's text as it came |
| `result` | the parsed answer, when there is one |
| `status`, `error`, `http_status` | `ok` or `error`, why, and the proxy's status |
| `latency_ms`, `input_tokens`, `output_tokens` | |

The task and tier keys are composite with `organization_id`, so a row
cannot name another organisation's. The request, response, result and
error are redacted (`internal/redact`) before they are stored: with the
proxy's key (`DUDE_LLM_KEY`), the tier's header values, and, for a
question about a task, its project's secret values. Values shorter than
`redact.MinLength` (6 bytes) are not redacted. The model itself is sent
the text unredacted.

## The API

| Route | Who | |
| --- | --- | --- |
| `GET /v1/judgements/settings` | members | `{tierId, canEdit}` |
| `PUT /v1/judgements/settings` | admins | `{tierId: string \| null}`; 400 for a tier not the organisation's |
| `GET /v1/judgements?asker=&limit=50&before=` | members | the last 7 days, newest first: `{calls, summary: {calls, since}, askers, next}` |
| `GET /v1/judgements/:id` | members | the call plus `request` and `responseText` |
| `POST /v1/judgements/test` | admins | `{tierId}`, forwarded to the orchestrator |

`POST /internal/judgements/test` (orchestrator) asks the fixed question
"Is the sky blue on a clear day?" of the tier named — so an admin can try a
tier before saving it — through the same request and parsing as Judge, and
records nothing: `{ok, decision, confidence, reasoning, model, latencyMs,
inputTokens, outputTokens, error}`.

## Deliberately not built

- **Callers.** Nothing calls `Judge`: not delivery, escalations, CI or
  steering. Each will name its asker, its threshold and its fallback.
- **A tier per project, or per kind of question.** One tier per
  organisation; the page has room for more when a caller needs it.
- **Retention.** Calls are kept; the page reads 7 days. No sweep yet.
- **Cost.** The proxy returns no price and dude has no price table: the
  page shows tokens, not money.
