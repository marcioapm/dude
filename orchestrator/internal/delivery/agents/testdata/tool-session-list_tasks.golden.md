# list_tasks

A linked project's epics and tasks, with their keys (like BL-12), status and who asked for them — optionally only those mentioning some text.

```json
{
  "type": "object",
  "properties": {
    "project": {
      "type": "string",
      "description": "a linked project's key (like BL); optional when only one is linked"
    },
    "text": {
      "type": "string",
      "description": "only tasks whose title or goal mention this"
    }
  },
  "additionalProperties": false
}
```
