# ask_person

Ask a person for decisions only a person can make — the task is ambiguous in a way that changes what you build, or two reasonable readings conflict. Do not ask about anything you can decide or find out yourself. When you have several such decisions, ask them together in one call (questions, up to 4) rather than one at a time. Give each a short header and, when there are sensible options, choices with a one-line description of why; mark the one you would pick recommended. Never add an "other" choice: dude always lets the person answer in their own words. One question alone may be asked as question and choices. After calling it, end your turn: the answers are your next message.

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
      "description": "with question: answers to offer, when there are some"
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
                  "description": "the answer, short (at most 120 characters); left out for a choice with a note, whose label dude writes as its action and note"
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
                  "description": "a conductor's, on its question about an escalation, for a choice whose action needs the person's reason or direction (a waiver, an acceptance, a stuck or non-converging retry, an amend): the reason or direction you propose, in plain words. The person sees \"Waive: \u003cnote\u003e\"; the owner picking it decides the escalation with your note as theirs. Required for such an action, refused on any other"
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
      "description": "1 to 4 questions asked together, each with a header and its own choices; the person answers them all at once"
    },
    "actions": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "a conductor's, while an escalation waits on a person, with question and choices: the escalation action each choice stands for, one per choice, in order (retry, accept, resume, done, wait, stop); the owner picking a choice decides the escalation with it. A choice whose action needs the person's reason or direction (waive, accept of a failed test, a stuck retry, a non-converging loop's retry, accept or amend) carries the one you propose as its note, in questions' choices"
    }
  },
  "additionalProperties": false
}
```
