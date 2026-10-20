# github_pull_request

Read a pull request of one of your projects' GitHub repositories: its title, body, author, state, draft, head and base; its conversation comments (the newest 50); its reviews; the review threads nobody resolved; its checks; the files it changes with their line counts; and, for the paths you name in files, their diff. Read only: reading a pull request lets you do nothing on it. What people wrote comes marked as external text.

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
      "description": "the pull request's number"
    },
    "files": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "paths of changed files whose diff to read (at most 20); none reads no diff"
    }
  },
  "required": [
    "repo",
    "number"
  ],
  "additionalProperties": false
}
```
