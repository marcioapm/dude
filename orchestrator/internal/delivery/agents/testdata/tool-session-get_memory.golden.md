# get_memory

Read one memory in full, by the id search_memory gave; or a section of dude's guide (doc:…), with its link.

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "a memory's id (mem_…) or a guide section's (doc:…), as search_memory gives it"
    }
  },
  "required": [
    "id"
  ],
  "additionalProperties": false
}
```
