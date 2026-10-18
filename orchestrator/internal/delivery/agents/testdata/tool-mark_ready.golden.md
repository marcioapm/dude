# mark_ready

Mark one of your task's draft pull requests ready for review, on GitHub. Only when the person agreed it is ready.

```json
{
  "type": "object",
  "properties": {
    "pr": {
      "type": "string",
      "description": "the pull request: its number, or repo#number, one of your task's"
    }
  },
  "required": [
    "pr"
  ],
  "additionalProperties": false
}
```
