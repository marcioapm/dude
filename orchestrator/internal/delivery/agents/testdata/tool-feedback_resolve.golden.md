# feedback_resolve

Mark a person's feedback item fixed by your commit (a SHA; the CLI takes --commit HEAD). It holds the pull request until your work is published and the commit is found in it; a Run that ends without publishing reopens it. With several repositories, name the repository of a general item's commit.

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "the feedback item (fbi_…)"
    },
    "commit": {
      "type": "string",
      "description": "the commit that fixed it: a SHA you committed in this Run"
    },
    "repo": {
      "type": "string",
      "description": "the repository the commit is in, when the task changes several and the item names none"
    }
  },
  "required": [
    "id",
    "commit"
  ],
  "additionalProperties": false
}
```
