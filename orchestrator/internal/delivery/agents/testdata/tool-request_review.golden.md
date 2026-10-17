# request_review

Ask people for a review of one of your task's pull requests, on GitHub, as dude. Only the repository's collaborators: anyone else is refused and named. Refused for another task's pull request.

```json
{
  "type": "object",
  "properties": {
    "pr": {
      "type": "string",
      "description": "the pull request: its number, or repo#number, one of your task's"
    },
    "logins": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "GitHub logins to ask for a review; collaborators on the repository only"
    }
  },
  "required": [
    "pr",
    "logins"
  ],
  "additionalProperties": false
}
```
