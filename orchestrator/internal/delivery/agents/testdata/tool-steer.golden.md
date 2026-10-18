# steer

Steer a running phase Run of this task (implement, review, fix, simplify, test) as a person would: your words reach it at its next step, without stopping the work it is doing. Steer an agent going the wrong way, or to add something the person just said; start another phase only once a Run has ended. interrupt stops its turn so it hears this now: only when its current work is wasted. You are woken when it reads it, or if it never will. Allowed whoever takes the decisions; refused for a Run that ended, another task's or an earlier attempt's.

```json
{
  "type": "object",
  "properties": {
    "run": {
      "type": "string",
      "description": "the phase Run to steer (run_…), one of this task's current attempt, still running"
    },
    "text": {
      "type": "string",
      "description": "what to tell it: read at its next step, in the turn it is in"
    },
    "interrupt": {
      "type": "boolean",
      "description": "stop its current turn so it hears this now; only when the work it is doing is wasted"
    },
    "feedbackIds": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "people's open feedback items (fbi_…) to pass to an implementer or fixer: dude adds their own words, never a summary"
    }
  },
  "required": [
    "run"
  ],
  "additionalProperties": false
}
```
