# start_agent

Start an investigator child to research the linked repositories. The child is read-only, has no project secrets, cannot start another child, and may publish report artifacts to this session's Files. A session starts only investigators: leave profile out, or pass investigator. Use an optional prompt, note, tier, or idempotency key when needed.

```json
{
  "type": "object",
  "properties": {
    "profile": {
      "type": "string",
      "description": "only investigator; may be left out, and is investigator then"
    },
    "prompt": {
      "type": "string",
      "description": "additional instructions for the child"
    },
    "note": {
      "type": "string",
      "description": "the note passed to the child"
    },
    "tier": {
      "type": "string",
      "description": "the model tier for the child, if one is requested"
    },
    "key": {
      "type": "string",
      "description": "an idempotency key for this child request, if one is needed; the same key returns the same child"
    }
  },
  "additionalProperties": false
}
```
