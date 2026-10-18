# name_session

Name this session: one line of plain words, at most 60 characters, once its subject is clear. Name it again only if the subject clearly changes. Refused once a member has named it.

```json
{
  "type": "object",
  "properties": {
    "title": {
      "type": "string",
      "description": "the session's name: one line, at most 60 characters, plain words"
    }
  },
  "required": [
    "title"
  ],
  "additionalProperties": false
}
```
