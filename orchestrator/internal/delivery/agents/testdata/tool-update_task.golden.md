# update_task

Write what Chat settled into the task: its goal, its acceptance criteria, or both (criteria replace the list). Do it before you start the implementer, whenever you and the person agreed something the task's text does not say, so the implementer's prompt has it. Refused once an implementer has started on this attempt. Its relations (addRelations, removeRelations: blocked_by, blocks, related, by task key) may change at any status, when you learn this needs other work merged first; you remove only relations this task's agents added.

```json
{
  "type": "object",
  "properties": {
    "goal": {
      "type": [
        "null",
        "string"
      ],
      "description": "the task's goal, whole, as agreed"
    },
    "acceptanceCriteria": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "the task's acceptance criteria, the whole list, as agreed"
    },
    "addRelations": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "object",
        "properties": {
          "kind": {
            "type": "string",
            "description": "blocked_by, blocks or related, read from this task"
          },
          "task": {
            "type": "string",
            "description": "the other task's key (CP-41)"
          },
          "why": {
            "type": "string",
            "description": "why, shown quoted in the task's history"
          }
        },
        "required": [
          "kind",
          "task"
        ],
        "additionalProperties": false
      },
      "description": "relations to add to this task, at any status: blocked_by (it can't start before that task is merged), blocks, related"
    },
    "removeRelations": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "object",
        "properties": {
          "kind": {
            "type": "string",
            "description": "blocked_by, blocks or related, read from this task"
          },
          "task": {
            "type": "string",
            "description": "the other task's key (CP-41)"
          },
          "why": {
            "type": "string",
            "description": "why, shown quoted in the task's history"
          }
        },
        "required": [
          "kind",
          "task"
        ],
        "additionalProperties": false
      },
      "description": "relations to remove: only ones this task's agents added"
    }
  },
  "additionalProperties": false
}
```
