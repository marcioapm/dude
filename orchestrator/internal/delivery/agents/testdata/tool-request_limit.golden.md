# request_limit

Ask the task's people to raise one of its delivery's limits, for this task alone, when you hit it and the work needs more: the pull request fix rounds per review round (perReviewRound, the delivery policy's), the fix rounds per pull request (perPullRequest, the organisation's GitHub settings'), or one more failed-check rerun on a pull request's head (rerun). dude asks them with your reason and the choices to allow it or not; only someone who may change that setting can approve (an organisation admin for the fix rounds; the task's owner or an admin for a rerun). Approved, the check allows exactly what was granted, and limits shows it; the setting itself does not change. It is your one open question: end your turn after it, the answer is your next message. One request per limit at a time.

```json
{
  "type": "object",
  "properties": {
    "limit": {
      "type": "string",
      "description": "perReviewRound, perPullRequest or rerun"
    },
    "to": {
      "type": "integer",
      "description": "for perReviewRound and perPullRequest: the limit you ask for, above what limits shows now, by at most 10"
    },
    "pr": {
      "type": "string",
      "description": "for rerun: the pull request, as repo#number or number; the task's only open one when left out"
    },
    "reason": {
      "type": "string",
      "description": "why the task needs it, in a sentence or two the person decides on (at most 2000 characters)"
    }
  },
  "required": [
    "limit",
    "reason"
  ],
  "additionalProperties": false
}
```
