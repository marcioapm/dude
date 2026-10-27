# ask_person

Ask a person for decisions only a person can make — the task is ambiguous in a way that changes what you build, or two reasonable readings conflict. Do not ask about anything you can decide or find out yourself. When you have several such decisions, ask them together in one call (questions, up to 4) rather than one at a time. Give each a short header and, when there are sensible options, choices with a one-line description of why; mark the one you would pick recommended. Never add an "other" choice: dude always lets the person answer in their own words. One question alone may be asked as question and choices. Then end your turn: the answers are your next message. With to (a member's name), only that member can answer; what others say meanwhile reaches you with the answer.

```json
{
  "type": "object",
  "properties": {
    "question": {
      "type": "string",
      "description": "one question, with enough context to answer it; or use questions instead"
    },
    "choices": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "with question: answers to offer"
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
            "type": "string",
            "description": "a few words naming it, shown as its tab (at most 24 characters), like Retry scope"
          },
          "question": {
            "type": "string",
            "description": "the question, with enough context to answer it (at most 4000 characters)"
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
                  "type": "string",
                  "description": "the answer, short (at most 120 characters)"
                },
                "description": {
                  "type": "string",
                  "description": "one line on what it means or why (at most 300 characters)"
                },
                "recommended": {
                  "type": "boolean",
                  "description": "the one you would pick, shown as your suggestion; at most one per question"
                },
                "note": {
                  "type": "string",
                  "description": "not used here: a note is a task conductor's, on its question about an escalation"
                }
              },
              "additionalProperties": false
            },
            "description": "0 to 6 answers to offer, when there are sensible options; never an other or something-else choice: dude adds one"
          },
          "multiple": {
            "type": "boolean",
            "description": "the person may pick several choices (default one)"
          }
        },
        "required": [
          "header",
          "question"
        ],
        "additionalProperties": false
      },
      "description": "1 to 4 questions asked together, each with a header and its own choices; answered all at once"
    },
    "to": {
      "type": "string",
      "description": "a member's name: only they answer; others' messages meanwhile come with the answer"
    }
  },
  "additionalProperties": false
}
```
