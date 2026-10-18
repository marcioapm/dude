# resolve_thread

Resolve a review thread on one of your task's pull requests, once what it asked for is done (say so in it first, with reply_on_pull_request in_reply_to the thread's comment). A fixer answering pull request feedback resolves only the threads in its feedback. Refused for another pull request's thread.

```json
{
  "type": "object",
  "properties": {
    "pr": {
      "type": "string",
      "description": "the pull request: its number, or repo#number, one of your task's"
    },
    "thread": {
      "type": "string",
      "description": "the review thread: its GitHub id (PRRT_…), or the id of a line comment in it (line-comment-…)"
    }
  },
  "required": [
    "pr",
    "thread"
  ],
  "additionalProperties": false
}
```
