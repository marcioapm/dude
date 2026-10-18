# note

Pin a note on your epic: a short working fact every agent on the epic must follow — a contract, a decision, where the docs are. Every agent on the epic is told at its next step, and every Run that starts there reads it in its briefing, reviewers included. Notes are never edited: replaces N pins a new note in note N's place. At most 10 current notes, at most 2,000 characters each; a replace never counts against the limit.

```json
{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "description": "the note: a short working fact every agent on the epic must follow (at most 2,000 characters)"
    },
    "replaces": {
      "type": "integer",
      "description": "the number of the current note this one replaces"
    }
  },
  "required": [
    "text"
  ],
  "additionalProperties": false
}
```
