# create_task

Record a piece of work you found that is outside your task — a bug, a follow-up, a part to split out — as a new task in this project. It is not started: a person reads it and decides. The goal is required (at least 16 characters): say why it matters and what should change. A conductor or investigator may relate it (blockedBy, blocks, related: task keys); a refused relation refuses the whole create.

```json
{
  "type": "object",
  "properties": {
    "title": {
      "type": "string",
      "description": "what should change, in one line"
    },
    "goal": {
      "type": "string",
      "description": "required: why it matters, what should change, and any detail another agent or a person needs; see the tool's description for the minimum"
    },
    "acceptanceCriteria": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "things that must be true when it is done"
    },
    "epic": {
      "type": "string",
      "description": "an existing epic's title to put it in (see list_tasks); none leaves it outside any"
    },
    "blockedBy": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "conductor, investigator: task keys (CP-41) the new task can't start before they are merged"
    },
    "blocks": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "conductor, investigator: task keys that can't start before the new task is merged"
    },
    "related": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "conductor, investigator: task keys related to it, gating nothing"
    }
  },
  "required": [
    "title",
    "goal"
  ],
  "additionalProperties": false
}
```
