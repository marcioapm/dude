# pull_requests

Your task's pull requests: state, head commit, checks (each one's status), review (each reviewer's word), unresolved threads, and the feedback people left — its id, author, kind, path, a short excerpt (cut, when it leaves words out), and whether a fixer was sent it. Name feedback ids (at most 20) to read those in full, each as last edited.

```json
{
  "type": "object",
  "properties": {
    "feedback": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "string"
      },
      "description": "feedback items to read in full, by the id the list gives (issue-comment-…, line-comment-…, review-…); empty lists the pull requests with excerpts only"
    }
  },
  "additionalProperties": false
}
```
