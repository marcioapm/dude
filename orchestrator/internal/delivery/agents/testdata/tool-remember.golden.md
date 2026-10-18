# remember

Save something worth knowing next time, for every agent and person on this project: a fact that holds ("the billing API paginates by cursor"), a procedure that works, a trap and its way around. It is live at once, and marked as yours. Search first so you do not save it twice; do not save what the code or the task already says.

```json
{
  "type": "object",
  "properties": {
    "title": {
      "type": "string",
      "description": "one line that says what it is, as another agent would scan it in a list"
    },
    "content": {
      "type": "string",
      "description": "the fact, procedure or note, in Markdown: what, and why it holds"
    },
    "kind": {
      "type": "string",
      "description": "fact (default), procedure (steps that work) or note"
    },
    "about": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "task keys (TEXT-12) or epic titles this is about, in this project"
    },
    "scope": {
      "type": "string",
      "description": "project (default): this project only; organization: true in every project"
    }
  },
  "required": [
    "title",
    "content"
  ],
  "additionalProperties": false
}
```
