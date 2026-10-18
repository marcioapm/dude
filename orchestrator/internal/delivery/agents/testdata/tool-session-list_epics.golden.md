# list_epics

A linked project's epics, in priority order, with how many tasks each has and how many are still open.

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
