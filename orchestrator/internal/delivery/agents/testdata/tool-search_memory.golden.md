# search_memory

Search what is known here: memories people and agents saved (facts, procedures, notes), and this project's tasks, epics and the project itself — by words and by meaning, best first. Search before you investigate something that may already be known, and before you remember something.

```json
{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "what you want to know, in words: a question or the terms you would expect"
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
