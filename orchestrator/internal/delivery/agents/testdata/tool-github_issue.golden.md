# github_issue

Read an issue of one of your projects' GitHub repositories: its title, body, author, state and labels, and its comments in order (the first 50). Read only; what people wrote comes marked as external text. A pull request's conversation is read with github_pull_request.

```json
{
  "type": "object",
  "properties": {
    "repo": {
      "type": "string",
      "description": "the repository, owner/name, one of your projects'"
    },
    "number": {
      "type": "integer",
      "description": "the issue's number"
    }
  },
  "required": [
    "repo",
    "number"
  ],
  "additionalProperties": false
}
```
