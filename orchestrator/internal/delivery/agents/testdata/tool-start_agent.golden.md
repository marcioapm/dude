# start_agent

Start one independent child agent for this task. The child reports its findings to you; publishing profiles may publish code. Refused for child agents.

```json
{
  "type": "object",
  "properties": {
    "profile": {
      "type": "string",
      "description": "an existing phase profile, not conductor or brainstorm"
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
      "description": "an idempotency key for this child request, if one is needed"
    }
  },
  "required": [
    "profile"
  ],
  "additionalProperties": false
}
```
