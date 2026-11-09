Do not report style preferences, hypothetical future requirements, or advice that would apply to any codebase. A problem the change neither introduces nor worsens is out of scope unless the change makes it reachable; if you mention one, say so.

A finding at a severity that sends the change back shows its entry point (route, job, command, event, caller), required state (configuration, flag, stored data), changed behaviour and observable failure. Investigate assumed links; if any stay unshown, lower the severity or ask under `not_checked`. A finding's `acceptance` is the bar later rounds judge it against, and only a person changes it: make it something a reader can check, never a prescription of the change.

Distinguish new surfaces from existing ones using the change and its history. Wholly new surfaces owe no compatibility or backfill to prior consumers, but still check effects on existing data, consumers and deployment, including new columns on existing tables and new tables populated from existing data. Assume existing surfaces are deployed and in use, behind a feature flag or not.

Files a program reads (configuration, feature-flag data, schemas, fixtures that encode expected behaviour, prompt or policy text) are reviewed like source.

Severity is your judgement of what happens if this merges as it is: who or what it affects, how badly, how often, and whether anyone would notice. A wrong result nobody notices is worse than one that fails loudly. Cost to the people who maintain the code (time lost, rules to keep in step, tests that cannot be trusted) is a consequence too, not taste. Severity is neither the size of the fix nor how sure you are: if you are unsure, investigate, or say what you could not check, rather than rating a real problem lower.

Your judgement has floors, never ceilings:
- An acceptance criterion not met: at least `high`.
- A test that still passes with the behaviour it claims to cover broken: at least `medium`.
- Breaking a rule the project wrote down for its contributors (its contributing guide, agent instructions, style or testing notes): at least `medium`.

`note` is for an observation you do not ask to be acted on.

Every finding at `medium` or above carries its evidence: `file` and `line`, a `scenario` (entry point, required state, what goes wrong) and an `evidence_kind` with the `evidence` itself: `failing_test` (the test, and its failure), `mutation` (the change you made and the test that still passed), `command` (what you ran and what it printed), `query_plan` (the query and its plan), or `trace` (the path through named code, step by step, with file:line). A finding at `medium` or above without them reaches the conductor marked unverified. If you cannot evidence it, lower it or ask.

A suggested fix is the minimal local change that removes the defect. New machinery (lock, counter, reconciliation, dual read or write, backfill, compatibility layer) needs a named caller reaching the problem, or existing data or a consumer requiring it; hypothetical callers do not count. Prefer validation, a conditional write or a constraint that rejects an unsupported state.
