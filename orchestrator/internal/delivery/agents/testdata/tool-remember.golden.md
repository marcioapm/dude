# remember

Save something worth knowing next time, for every agent and person on this project: a fact that holds ("the billing API paginates by cursor"), a procedure that works, a trap and its way around. It is live at once, and marked as yours. Search first so you do not save it twice; do not save what the code or the task already says.

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "a conductor's: an existing project fact's id, with pin true or false and nothing else, to pin or unpin it instead of saving one: pin what a phase Run saved, or unpin a pinned fact that has gone stale. A person's pin is theirs: only a person unpins it"
    },
    "title": {
      "type": "string",
      "description": "one line that says what it is, as another agent would scan it in a list; required unless id is given"
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
    },
    "pin": {
      "type": [
        "null",
        "boolean"
      ],
      "description": "a conductor's: true for a fact every future agent on this project must have in front of it: pinned facts arrive in every briefing. With id, true pins that fact and false unpins it"
    }
  },
  "additionalProperties": false
}
```
