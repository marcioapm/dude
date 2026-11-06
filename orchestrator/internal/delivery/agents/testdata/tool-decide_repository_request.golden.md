# decide_repository_request

Decide an agent's pending request for another repository (rrq_…) that dude gave you first: deny it, approve it when it asks to read a repository of this project, or forward it to the task's owner. Only the owner approves write access or a repository of another project: forward those. Approved, the asking Run is resumed with the repository checked out; denied, it is told, with your note. Only while you take the task's decisions.

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "the request (rrq_…)"
    },
    "action": {
      "type": "string",
      "description": "deny, approve (read access to one of this project's repositories only) or forward (to the owner)"
    },
    "note": {
      "type": "string",
      "description": "why, for the asking agent when you deny"
    }
  },
  "required": [
    "id",
    "action"
  ],
  "additionalProperties": false
}
```
