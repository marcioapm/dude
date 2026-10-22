# answer_review_question

Answer a reviewer's open question on this task (rvq_…) with a fact you established from the code, its configuration or its history, or withdraw it as moot with the reason. The next reviewer of its category is shown your answer as the conductor's, never as a person's decision; what only a person can say, ask them (ask_person) and let them answer it on the task. Only while you take the task's decisions.

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "the question (rvq_…)"
    },
    "answer": {
      "type": "string",
      "description": "the fact, and where you established it; leave out to withdraw"
    },
    "withdraw": {
      "type": "boolean",
      "description": "true to withdraw the question as moot instead of answering it"
    },
    "reason": {
      "type": "string",
      "description": "why it no longer matters, with withdraw"
    }
  },
  "required": [
    "id"
  ],
  "additionalProperties": false
}
```
