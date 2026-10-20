# pull_requests

A linked project's pull requests: with task, that task's in full (checks, reviews, feedback; name feedback ids to read those items' whole text); without, the project's latest, one line each.

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
    },
    "feedback": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "with task, feedback items to read in full (at most 20), by the id its list gives"
    }
  },
  "additionalProperties": false
}
```
