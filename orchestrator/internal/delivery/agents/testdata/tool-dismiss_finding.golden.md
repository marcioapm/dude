# dismiss_finding

Leave an open review finding as it is, with the reason — shown with the finding, as a person accepting one is. Only while you take the task's decisions.

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "the finding (fnd_…)"
    },
    "reason": {
      "type": "string",
      "description": "why it is left as it is, shown with the finding"
    }
  },
  "required": [
    "id",
    "reason"
  ],
  "additionalProperties": false
}
```
