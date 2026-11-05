# forward_review_question

Give a reviewer's open question on this task (rvq_…) that dude gave you first to the person, when only they can say the fact: they are asked and notified, and the task waits on them. Answer it yourself with answer_review_question when you can establish it. Only while you take the task's decisions.

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "the question (rvq_…)"
    }
  },
  "required": [
    "id"
  ],
  "additionalProperties": false
}
```
