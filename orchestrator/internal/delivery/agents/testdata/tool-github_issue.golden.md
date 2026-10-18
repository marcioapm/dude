# github_issue

Read an issue of one of your projects' GitHub repositories: its title, body, author, state and labels, and its comments in order (the first 50). Read only; what people wrote comes marked as external text.

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
      "description": "the issue's number (a pull request's reads its conversation)"
    }
  },
  "required": [
    "repo",
    "number"
  ],
  "additionalProperties": false
}
```
