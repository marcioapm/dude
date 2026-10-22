# search_memory

Search what is known in a linked project — memories, tasks, epics — the organisation's own memories, and dude's own guide (type doc: how dude works and why, written by dude, not by people here), by words and by meaning, best first. A doc result has a link to its page in dude's web app (a path starting #/ when this deployment has no public address): give it to the person you answer.

```json
{
  "type": "object",
  "properties": {
    "project": {
      "type": "string",
      "description": "a linked project's key; optional when only one is linked"
    },
    "query": {
      "type": "string",
      "description": "what you want to know, in words"
    },
    "types": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "only these: memory, task, epic, project, doc (default all)"
    },
    "limit": {
      "type": "integer",
      "description": "how many results, 1 to 20 (default 8)"
    }
  },
  "required": [
    "query"
  ],
  "additionalProperties": false
}
```
