# start_phase

Take the decision the delivery waits on by starting a phase from the task's head: implement, review (some or all categories), fix (some or all open findings; at pull request feedback, the feedback), simplify or test. The workflow creates the Runs as it creates its own, waits for them, and wakes you at the next decision. While the delivery waits on its pull requests with nothing running, fix (say what, in note), review or test start too, on the pull request's branch, and it waits on them again after. fix (or simplify) may carry then: dude starts the review you name as soon as the fix has committed, without waking you, and wakes you after that review (or right after the fix, wake: after_fix); a fix that fails, commits nothing, reports itself partial or blocked, or asks a person wakes you at once. Refused when you do not take the task's decisions, while a Run or publish is changing the branch, or past a pull request's fix rounds: then ask the person. No review round or fix attempt bound refuses one.

```json
{
  "type": "object",
  "properties": {
    "phase": {
      "type": "string",
      "description": "implement, review, fix, simplify or test"
    },
    "categories": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "review: the reviewers to run (correctness, quality, tests, security, database, api, frontend, documentation, performance); none runs those the change warrants"
    },
    "findings": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "fix: the open findings to fix (fnd_…); none fixes every open one"
    },
    "note": {
      "type": "string",
      "description": "what you ask of the Run, added to its prompt"
    },
    "round": {
      "type": [
        "null",
        "object"
      ],
      "properties": {
        "scope": {
          "type": "string",
          "description": "whole: the whole branch, in any round; fix (the default): round 1 whole, later rounds only what changed since the last round"
        },
        "bar": {
          "type": "string",
          "description": "your bar for this task's fixes, if looser or stricter than every medium or above plus trivial lows; quoted to the fixer and the reviewers as your words (up to 1000 characters)"
        }
      },
      "description": "review, fix or simplify: the review round's scope and your bar for it",
      "additionalProperties": false
    },
    "then": {
      "type": [
        "null",
        "object"
      ],
      "properties": {
        "review": {
          "type": [
            "null",
            "array"
          ],
          "items": {
            "type": "string"
          },
          "description": "required: the reviewers to run (as categories)"
        },
        "wake": {
          "type": "string",
          "description": "after_review (default): wake you after the review; after_fix: wake you after the fix, the review queued as decide next"
        },
        "note": {
          "type": "string",
          "description": "what you ask of the reviewers"
        }
      },
      "description": "fix or simplify: the review to start once it has committed, with no decision between",
      "required": [
        "review"
      ],
      "additionalProperties": false
    }
  },
  "required": [
    "phase"
  ],
  "additionalProperties": false
}
```
