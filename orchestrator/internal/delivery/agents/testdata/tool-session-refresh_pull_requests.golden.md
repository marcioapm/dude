# refresh_pull_requests

Read a linked project's task's open pull requests from GitHub now, and list them as pull_requests with that task does. pull_requests answers dude's last read of GitHub (lastPolledAt); use this when that is too old. Each call is one of the session's 60 GitHub reads an hour.

```json
{
  "type": "object",
  "properties": {
    "project": {
      "type": "string",
      "description": "a linked project's key (like BL); optional when only one is linked"
    },
    "task": {
      "type": "string",
      "description": "a task's key (like BL-58) in that project"
    }
  },
  "required": [
    "task"
  ],
  "additionalProperties": false
}
```
