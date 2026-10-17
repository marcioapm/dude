# get_memory

Read one memory in full, by the id search_memory gave, with where it was learned and what it is about.

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "a memory's id, as search_memory gives it (mem_…)"
    }
  },
  "required": [
    "id"
  ],
  "additionalProperties": false
}
```
