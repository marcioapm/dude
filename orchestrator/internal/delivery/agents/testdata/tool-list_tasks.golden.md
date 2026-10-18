# list_tasks

The project's epics and tasks, with their keys (like TEXT-12), status and who asked for them, and each one's relations: what it is blocked by (and whether that merged), what it blocks, what it is related to, and whether it can start — optionally only those mentioning some text, or only those that can start (startable). Use it before creating work, to find what already exists. With peers: true, your peers instead (the other tasks on your epic): each one's phase, whether its Run is live or parked, branch and head, files changed, its agent's last words, and its messages waiting for you.

```json
{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "description": "only tasks whose title or goal mention this (case-insensitive); empty lists everything"
    },
    "peers": {
      "type": "boolean",
      "description": "your peers instead: the other tasks on your epic, each with its phase, live or parked, branch and head, files changed, its agent's last words and the messages it has waiting for you; your own task is marked self"
    },
    "startable": {
      "type": "boolean",
      "description": "only tasks not started with nothing unmerged in front of them"
    }
  },
  "additionalProperties": false
}
```
