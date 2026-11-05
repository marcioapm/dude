# answer_question

Answer an open conductor-addressed question from a current Run of this task with facts you know or established. Supply id and either answer (one question) or answers (one per item, choices as zero-based indices and/or text). The writer receives your answer once. Closed, stale, person-addressed and other-task questions are refused. If only the owner can decide, ask them with ask_person, wait for their answer, then answer the writer yourself; automatic forwarding is not available yet.

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string"
    },
    "answer": {
      "type": "string"
    },
    "answers": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "object",
        "properties": {
          "choices": {
            "type": [
              "null",
              "array"
            ],
            "items": {
              "type": "integer"
            }
          },
          "text": {
            "type": "string"
          }
        },
        "required": [
          "choices",
          "text"
        ],
        "additionalProperties": false
      }
    }
  },
  "required": [
    "id"
  ],
  "additionalProperties": false
}
```
