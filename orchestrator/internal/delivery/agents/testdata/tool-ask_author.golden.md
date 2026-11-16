# ask_author

Ask the pull request's author a question on GitHub, as dude: a fact only they know (intent, "is this called from X?"), never what the code answers, and never scope or a waiver (those are the owner's: ask_person). One question per thing. With finding_id it is asked in dude's thread on that finding, else on the conversation; either way it @mentions the author. The question stays open, and holds dude's approval (its review says "Before approving: …"), until you close it with answer_review_question (its id) or it times out. The author's answer reaches you as a message from the pull request. A review-only task's conductor only; refused elsewhere. GitHub refusing it withdraws it.

```json
{
  "type": "object",
  "properties": {
    "pr": {
      "type": "string",
      "description": "the pull request: its number, repo#number, owner/repo#number or URL, your task's"
    },
    "question": {
      "type": "string",
      "description": "the question, in Markdown, as you would ask it in a review: one thing"
    },
    "finding_id": {
      "type": "string",
      "description": "the finding the question is about: asked in dude's thread on it (findings lists the findings dude commented on inline)"
    }
  },
  "required": [
    "pr",
    "question"
  ],
  "additionalProperties": false
}
```
