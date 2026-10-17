# search_memory

Search what is known in a linked project — memories, tasks, epics — and the organisation's own memories, by words and by meaning, best first.

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
      "description": "only these: memory, task, epic, project (default all)"
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
