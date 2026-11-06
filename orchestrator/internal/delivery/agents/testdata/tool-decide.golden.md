# decide

Take the decision the delivery waits on: next (what Deliver would do now), ask_person (a question for the person: the note, with choices; before the pull request, always the fixed question Open / Draft / Show me the diff / Another round, the note its context, Open recommended unless you recommend another), wait (at pull request feedback: leave it), or open_pull_request — accepted only at the decision before the pull request, after the person answered Open or Draft to that question at the task's current head, or picked a choice with action open or draft on your earlier question in this delivery that still holds (the same attempt, nothing failing that they were not asked about, no commits since but the simplifier's). The result and the ledger cite that answer. Never open without a person's answer; do not ask again when it still holds.

```json
{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "description": "next, ask_person, wait or open_pull_request"
    },
    "note": {
      "type": "string",
      "description": "why; for ask_person, the question (before the pull request, its context)"
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
          "action": {
            "type": "string",
            "description": "open or draft: picking it authorizes open_pull_request (as a draft for draft) at the decision before the pull request, while it holds; none for any other answer"
          },
          "recommended": {
            "type": "boolean",
            "description": "the one you would pick, at most one"
          }
        },
        "required": [
          "label"
        ],
        "additionalProperties": false
      },
      "description": "ask_person away from the pull request's decision: 2 to 6 answers to offer, never as prose in the note; none for a question answered in the person's own words. Before the pull request the choices are fixed: leave this out"
    },
    "recommend": {
      "type": "string",
      "description": "ask_person before the pull request: the fixed choice you recommend (Open, Draft, Show me the diff or Another round; Open when left out)"
    }
  },
  "required": [
    "action"
  ],
  "additionalProperties": false
}
```
