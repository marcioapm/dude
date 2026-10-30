# recover_run

Push the commits an ended Run of this task never published — lux stopped it at its time limit, it crashed, or it was stopped — from lux's checkpoint of its checkout to a branch of its own. Only its committed work: what was not committed is lost. Only a Run that publishes (implementer, fixer, simplifier, a writer child); a reviewer's or tester's work is never pushed. Its commits are scanned for secrets first. The branch moves no task branch and opens no pull request, and is deleted with the task's other per-Run branches when the task ends: to use the work, start a writer to merge it, then review it as any change. You are woken with what was pushed, or why not. Refused for a live Run (stop it first), another task's, or a branch outside dude/<task>/recovered-.

```json
{
  "type": "object",
  "properties": {
    "run": {
      "type": "string",
      "description": "the ended Run (run_…) whose commits to recover"
    },
    "branch": {
      "type": "string",
      "description": "the branch to push to; default dude/\u003ctask\u003e/recovered-\u003crun\u003e; must start with dude/\u003ctask\u003e/recovered-"
    },
    "note": {
      "type": "string",
      "description": "why: kept with the record of the recovery"
    }
  },
  "required": [
    "run"
  ],
  "additionalProperties": false
}
```
