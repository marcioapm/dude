# remember

Save something worth knowing next time, for this session: only its members and its agent will find it. A fact that holds, a procedure that works, a trap and its way around. Search first.

```json
{
  "type": "object",
  "properties": {
    "title": {
      "type": "string",
      "description": "one line that says what it is"
    },
    "content": {
      "type": "string",
      "description": "the fact, procedure or note, in Markdown"
    },
    "kind": {
      "type": "string",
      "description": "fact (default), procedure or note"
    },
    "about": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "task keys in a linked project, or epic titles there (project:Title when more than one is linked)"
    }
  },
  "required": [
    "title",
    "content"
  ],
  "additionalProperties": false
}
```
