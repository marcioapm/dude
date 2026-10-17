# start_phase

Take the decision the delivery waits on by starting a phase from the task's head: implement, review (some or all categories), fix (some or all open findings; at pull request feedback, the feedback), simplify or test. The workflow creates the Runs as it creates its own, waits for them, and wakes you at the next decision. While the delivery waits on its pull requests with nothing running, fix (say what, in note), review or test start too, on the pull request's branch, and it waits on them again after. Refused when you do not take the task's decisions, while a Run or publish is changing the branch, or past the policy's bounds: then ask the person.

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
      "description": "review: the reviewers to run (correctness, tests, security, database, api, frontend, performance); none runs those the change warrants"
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
    }
  },
  "required": [
    "phase"
  ],
  "additionalProperties": false
}
```
