# pull_requests

A linked project's pull requests: with task, that task's in full (checks, reviews, feedback); without, the project's latest, one line each.

```json
{
  "type": "object",
  "properties": {
    "project": {
      "type": "string",
      "description": "a linked project's key (like BL); optional when only one is linked"
    },
    "task": {
      "type": "string",
      "description": "a task's key (like BL-58) in that project"
    },
    "ids": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "findings: with task, those to read in full (fnd_…)"
    }
  },
  "additionalProperties": false
}
```
