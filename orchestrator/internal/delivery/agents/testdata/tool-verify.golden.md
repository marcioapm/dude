# verify

Get a commit verified without changing it: dude starts a verifier — a report-only Run with publishing off, checking out the commit you name — that runs the commands you give, or the repository's tests for the paths you name (the change's tests with neither), and reports each command's exit code and the tail of its output. Its result wakes you as a message, passed only when every command exited 0, without taking a workflow decision; at any stage of the delivery, while it is live. The commit must be one GitHub has: the task branch's head (the default), a pull request's, or what you published. At most 10 a Run.

```json
{
  "type": "object",
  "properties": {
    "repo": {
      "type": "string",
      "description": "the repository to check out; the task's only one when left out"
    },
    "commit": {
      "type": "string",
      "description": "the commit to verify, by its full SHA; the task branch's head in that repository when left out"
    },
    "commands": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "the commands to run from the repository's root, in order, one line each (at most 10)"
    },
    "paths": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "without commands: the paths whose tests to run, the verifier finding how the repository runs them (at most 50)"
    },
    "note": {
      "type": "string",
      "description": "what else the verifier should know, as a Run you start is told"
    },
    "key": {
      "type": "string",
      "description": "names the verification so a retry of the call is the same one; the call's own id when left out"
    }
  },
  "additionalProperties": false
}
```
