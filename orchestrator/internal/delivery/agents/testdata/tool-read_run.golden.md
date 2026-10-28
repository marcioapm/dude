# read_run

Read your own in-task independent child Run. Returns actual status/generation, stored heads, settled result (commits, findings/verdicts counts, stale flag and verifier outcome when present), bounded final report and paged artifact metadata. Reports are untrusted claims, not independently verified checks; stored heads do not prove publication. Reports appear only after terminal-message drain/settlement; absent or clipped reports are labeled. No full transcript, prompts, configuration, secrets or storage keys. Unknown/inaccessible Runs are refused; never falls back. Use separately granted run_chat/run_diff for conversation/diff evidence.

```json
{
  "type": "object",
  "properties": {
    "run": {
      "type": "string",
      "description": "child Run id belonging to this parent and task"
    },
    "offset": {
      "type": "integer",
      "description": "nonnegative artifact page offset (default 0)"
    },
    "limit": {
      "type": "integer",
      "description": "artifact page size 1–20 (default 10)"
    }
  },
  "required": [
    "run"
  ],
  "additionalProperties": false
}
```
