# start_agents

Start a durable cohort of 1–8 independent child agents atomically. Uses start_agent profiles, briefs, notes and tier overrides with the same admission guards. Any refusal creates no children. Receive one digest after every current member settles, including failures and aborts, not one input per member. Conductor only; never bypasses fixed review policy.

```json
{
  "type": "object",
  "properties": {
    "runs": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "object",
        "properties": {
          "profile": {
            "type": "string"
          },
          "prompt": {
            "type": "string"
          },
          "note": {
            "type": "string"
          },
          "tier": {
            "type": "string"
          },
          "key": {
            "type": "string"
          }
        },
        "required": [
          "profile"
        ],
        "additionalProperties": false
      },
      "description": "1–8 complete child briefs, each with profile, prompt, note and optional tier; per-member keys are unsupported, use the cohort key"
    },
    "key": {
      "type": "string",
      "description": "idempotency key scoped to this parent Run; replay returns persisted membership"
    }
  },
  "required": [
    "runs"
  ],
  "additionalProperties": false
}
```
