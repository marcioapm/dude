# start_agent

Start a named investigator child to research the linked repositories. The child is read-only, has no project secrets, cannot start another child, and may publish report artifacts to this session's Files. Use an optional prompt, note, tier, or idempotency key when needed.

```json
{
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
}
```
