# list_repositories

A linked project's repositories: which are checked out for this session (read only, and where), and which are not.

```json
{
  "type": "object",
  "properties": {
    "project": {
      "type": "string",
      "description": "a linked project's key (like BL); optional when only one is linked"
    }
  },
  "additionalProperties": false
}
```
