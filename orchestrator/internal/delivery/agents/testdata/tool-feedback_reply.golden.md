# feedback_reply

Answer a person's feedback item without changing the code: say why it is wrong, out of scope, or already so. It is then replied, which holds nothing back; the person may reopen it.

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "the feedback item (fbi_…)"
    },
    "text": {
      "type": "string",
      "description": "why you are not changing the code for it, to the person who wrote it"
    }
  },
  "required": [
    "id",
    "text"
  ],
  "additionalProperties": false
}
```
