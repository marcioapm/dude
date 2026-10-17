# run_diff

What a Run of your task changed: this Run's checkout, uncommitted work included, against the commit it started from — one snapshot, no history. Without paths, the changed files, most changed first, with line counts and no lines (paged by limit and offset; hasMore says there is another page). With paths, those files' changes as unified diff text, at most 2,000 lines in all. With task (a peer's key), that peer's code as dude last stored it, the same way.

```json
{
  "type": "object",
  "properties": {
    "run": {
      "type": "string",
      "description": "a Run of this task (run_…); empty is your own"
    },
    "task": {
      "type": "string",
      "description": "a peer's key (like ABS-2) instead of a Run: its current Run's stored diff, or its latest if none is going"
    },
    "paths": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "files to show the changes of, as the list names them (exact); empty lists the files"
    },
    "nameStatus": {
      "type": "boolean",
      "description": "list only each file's path and status (M, A, D, R): the cheapest form"
    },
    "limit": {
      "type": "integer",
      "description": "files per page of the list, 1 to 1000 (default 200)"
    },
    "offset": {
      "type": "integer",
      "description": "files of the list to skip, for the next page"
    }
  },
  "additionalProperties": false
}
```
