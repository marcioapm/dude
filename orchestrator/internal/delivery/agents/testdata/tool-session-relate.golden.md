# relate

Propose a relation between two existing tasks of linked projects, for a member to file: from blocks to (to can't start until from is merged), or related. You never write it yourself.

```json
{
  "type": "object",
  "properties": {
    "from": {
      "type": "string",
      "description": "an existing task's key (BL-41) in a linked project"
    },
    "to": {
      "type": "string",
      "description": "another existing task's key in a linked project"
    },
    "kind": {
      "type": "string",
      "description": "blocks (from can't let to start until from is merged) or related"
    },
    "why": {
      "type": "string",
      "description": "why, for the member who files it and the tasks' history"
    }
  },
  "required": [
    "from",
    "to",
    "kind"
  ],
  "additionalProperties": false
}
```
