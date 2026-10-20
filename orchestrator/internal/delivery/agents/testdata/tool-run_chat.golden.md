# run_chat

A Run's Chat, as people see it: its latest messages, oldest first — what its agent said, and what people, the conductor and GitHub told it (messages, steers, answers) — and the question it is waiting on, if any: what it asks, its choices, who answers it and when it was asked. Use it to relay a Run's question, or to see what a Run was told. Only your task's Runs (its children and earlier attempts included); for a session's agent, the Runs its session started.

```json
{
  "type": "object",
  "properties": {
    "run": {
      "type": "string",
      "description": "the Run's id (run_…)"
    },
    "limit": {
      "type": "integer",
      "description": "how many of the latest messages, 1 to 100; default 30"
    }
  },
  "required": [
    "run"
  ],
  "additionalProperties": false
}
```
