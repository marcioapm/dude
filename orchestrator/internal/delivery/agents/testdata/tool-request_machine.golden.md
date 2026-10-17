# request_machine

Move to a larger machine.

```json
{
  "type": "object",
  "properties": {
    "size": {
      "type": "string",
      "description": "one of the sizes listed in this tool's description, by name (Large)"
    },
    "reason": {
      "type": "string",
      "description": "what ran out: the command, its exit or error, the workers or threads it used"
    }
  },
  "required": [
    "size",
    "reason"
  ],
  "additionalProperties": false
}
```
