# ask_conductor

Ask your task's conductor up to four questions when you cannot proceed. Use the same question/choices or questions form as ask_person. End your turn; the answer arrives as your next message. If no conductor is available, dude asks the person instead. Actions and choice notes are conductor-only and are refused here.

```json
{
  "type": "object",
  "properties": {
    "question": {
      "type": "string"
    },
    "choices": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      }
    },
    "questions": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "object",
        "properties": {
          "header": {
            "type": "string"
          },
          "question": {
            "type": "string"
          },
          "choices": {
            "type": [
              "null",
              "array"
            ],
            "items": {
              "type": "object",
              "properties": {
                "label": {
                  "type": "string"
                },
                "description": {
                  "type": "string"
                },
                "recommended": {
                  "type": "boolean"
                },
                "note": {
                  "type": "string"
                }
              },
              "additionalProperties": false
            }
          },
          "multiple": {
            "type": "boolean"
          }
        },
        "required": [
          "header",
          "question"
        ],
        "additionalProperties": false
      }
    },
    "actions": {
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
}
```
