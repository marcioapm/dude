# propose

Put work on the session's proposal card, for a member to file with a click, as themselves: new epics and tasks in linked projects, edits to a task's goal or criteria (before/after), comments on a task, and notes for an epic: a new epic's notes, or a note on an existing one. A note is a short working fact every agent on the epic must follow (a contract, a decision, where the docs are); you never pin one yourself. A task item may say what it waits on and holds up (blockedBy, blocks, related): an existing task as {"key": "BL-41"}, or another item of this proposal as {"item": 1}, numbered from 1 as the card numbers them. Work out the order: filing a task files what it waits on with it. You never create, edit or comment yourself. A new proposal replaces the card.

```json
{
  "type": "object",
  "properties": {
    "items": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "object",
        "properties": {
          "kind": {
            "type": "string"
          },
          "project": {
            "type": "string"
          },
          "epic": {
            "type": "string"
          },
          "title": {
            "type": "string"
          },
          "goal": {
            "type": "string"
          },
          "acceptanceCriteria": {
            "type": [
              "null",
              "array"
            ],
            "items": {
              "type": "string"
            }
          },
          "description": {
            "type": "string"
          },
          "notes": {
            "type": [
              "null",
              "array"
            ],
            "items": {
              "type": "string"
            }
          },
          "task": {
            "type": "string"
          },
          "before": {
            "type": [
              "null",
              "object"
            ],
            "properties": {
              "goal": {
                "type": [
                  "null",
                  "string"
                ]
              },
              "acceptanceCriteria": {
                "type": [
                  "null",
                  "array"
                ],
                "items": {
                  "type": "string"
                }
              }
            },
            "additionalProperties": false
          },
          "after": {
            "type": [
              "null",
              "object"
            ],
            "properties": {
              "goal": {
                "type": [
                  "null",
                  "string"
                ]
              },
              "acceptanceCriteria": {
                "type": [
                  "null",
                  "array"
                ],
                "items": {
                  "type": "string"
                }
              }
            },
            "additionalProperties": false
          },
          "text": {
            "type": "string"
          },
          "replaces": {
            "type": "integer"
          },
          "blockedBy": {
            "type": [
              "null",
              "array"
            ],
            "items": {
              "type": "object",
              "properties": {
                "key": {
                  "type": "string"
                },
                "item": {
                  "type": "integer"
                }
              },
              "additionalProperties": false
            }
          },
          "blocks": {
            "type": [
              "null",
              "array"
            ],
            "items": {
              "type": "object",
              "properties": {
                "key": {
                  "type": "string"
                },
                "item": {
                  "type": "integer"
                }
              },
              "additionalProperties": false
            }
          },
          "related": {
            "type": [
              "null",
              "array"
            ],
            "items": {
              "type": "object",
              "properties": {
                "key": {
                  "type": "string"
                },
                "item": {
                  "type": "integer"
                }
              },
              "additionalProperties": false
            }
          },
          "from": {
            "type": "string"
          },
          "to": {
            "type": "string"
          },
          "relationKind": {
            "type": "string"
          },
          "why": {
            "type": "string"
          }
        },
        "required": [
          "kind"
        ],
        "additionalProperties": false
      },
      "description": "the card's items in order: {kind: epic, project, title, description, notes?: [text]} (notes pinned when filed); {kind: task, project, epic?, title, goal, acceptanceCriteria, blockedBy?, blocks?, related?}; {kind: edit, task, after: {goal?, acceptanceCriteria?}}; {kind: comment, task, text}; {kind: note, project, epic, text, replaces?} (replaces: a current note's number)"
    }
  },
  "required": [
    "items"
  ],
  "additionalProperties": false
}
```
